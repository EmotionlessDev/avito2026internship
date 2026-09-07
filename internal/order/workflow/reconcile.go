package workflow

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	sdk "go.temporal.io/sdk/workflow"
)

// ReconciliationWorkflowID — идентификатор процесса суточной сверки.
const ReconciliationWorkflowID = "menu-reconciliation"

// MenuReconciliationWorkflow раз в сутки сверяет меню платформы с тем,
// что отдаёт само заведение.
//
// Это ответ на вопрос «а что, если событие всё-таки потерялось»: периодическая
// сверка дешевле, чем попытка построить exactly-once (docs/DESIGN.md §6.4).
// Расхождения не чинятся автоматически — они логируются: тихая правка данных
// по расписанию прячет проблему интеграции вместо того, чтобы её показать.
func MenuReconciliationWorkflow(ctx sdk.Context) error {
	ctx = sdk.WithActivityOptions(ctx, activityOptions())

	var activities *ReconcileActivities

	var report ReconcileReport
	if err := sdk.ExecuteActivity(ctx, activities.ReconcileMenus).Get(ctx, &report); err != nil {
		return err
	}

	log := sdk.GetLogger(ctx)
	log.Info("сверка меню завершена",
		"venues", report.VenuesChecked,
		"drift", report.DriftCount)

	return nil
}

// ReconcileReport — итог сверки.
type ReconcileReport struct {
	VenuesChecked int `json:"venues_checked"`
	DriftCount    int `json:"drift_count"`
}

// ReconcileActivities сверяет каталог платформы с меню заведений.
type ReconcileActivities struct {
	menu   *merchant.Repository
	client *http.Client
}

// NewReconcileActivities собирает активности сверки.
func NewReconcileActivities(menu *merchant.Repository) *ReconcileActivities {
	return &ReconcileActivities{
		menu:   menu,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// ReconcileMenus обходит заведения с зарегистрированным вебхуком и сравнивает
// их меню с каталогом платформы.
func (a *ReconcileActivities) ReconcileMenus(ctx context.Context) (ReconcileReport, error) {
	log := activity.GetLogger(ctx)

	venues, err := a.menu.VenuesWithWebhook(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}

	report := ReconcileReport{}

	for _, venue := range venues {
		report.VenuesChecked++

		drift, err := a.reconcileVenue(ctx, venue)
		if err != nil {
			// Недоступное заведение — не повод валить сверку остальных:
			// это диагностика, а не операция над данными.
			log.Warn("заведение не ответило на сверку",
				"venue_id", venue.VenueID.String(), "error", err.Error())

			continue
		}

		report.DriftCount += drift
	}

	return report, nil
}

func (a *ReconcileActivities) reconcileVenue(ctx context.Context, venue merchant.WebhookTarget) (int, error) {
	remote, err := a.fetchMenu(ctx, venue)
	if err != nil {
		return 0, err
	}

	local, err := a.menu.MenuSnapshotFor(ctx, venue.VenueID)
	if err != nil {
		return 0, err
	}

	log := activity.GetLogger(ctx)
	drift := 0

	for externalID, item := range remote {
		platform, known := local[externalID]

		switch {
		case !known:
			drift++

			log.Warn("позиция есть у заведения, но не в каталоге",
				"venue_id", venue.VenueID.String(), "external_id", externalID)
		case platform.PriceMinor != item.PriceMinor || platform.IsAvailable != item.IsAvailable:
			drift++

			log.Warn("состояние позиции разошлось",
				"venue_id", venue.VenueID.String(), "external_id", externalID,
				"platform_price", platform.PriceMinor, "venue_price", item.PriceMinor,
				"platform_available", platform.IsAvailable, "venue_available", item.IsAvailable)
		}
	}

	for externalID := range local {
		if _, known := remote[externalID]; !known {
			drift++

			log.Warn("позиция есть в каталоге, но не у заведения",
				"venue_id", venue.VenueID.String(), "external_id", externalID)
		}
	}

	return drift, nil
}

// fetchMenu спрашивает меню у самого заведения.
//
// Запрос подписывается тем же секретом, что и вебхуки: для заведения это
// такой же входящий вызов платформы, и проверять его оно должно одинаково.
func (a *ReconcileActivities) fetchMenu(
	ctx context.Context, venue merchant.WebhookTarget,
) (map[string]merchant.MenuItemState, error) {
	url := strings.TrimSuffix(venue.URL, "/orders") + "/menu"
	timestamp := time.Now().UTC().Format(time.RFC3339)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("запрос меню заведения: %w", err)
	}

	request.Header.Set("X-Kitchen-Event-Id", uuid.New().String())
	request.Header.Set("X-Kitchen-Timestamp", timestamp)
	request.Header.Set("X-Kitchen-Signature", signMenuRequest(venue.Secret, timestamp))

	response, err := a.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("вызов заведения: %w", err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("заведение ответило %d: %w", response.StatusCode, errMenuUnavailable)
	}

	var payload struct {
		Items []struct {
			ExternalID  string `json:"external_id"`
			PriceMinor  int64  `json:"price_minor"`
			IsAvailable bool   `json:"is_available"`
		} `json:"items"`
	}

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("разбор меню заведения: %w", err)
	}

	items := make(map[string]merchant.MenuItemState, len(payload.Items))
	for _, item := range payload.Items {
		items[item.ExternalID] = merchant.MenuItemState{
			PriceMinor:  item.PriceMinor,
			IsAvailable: item.IsAvailable,
		}
	}

	return items, nil
}

// signMenuRequest подписывает запрос без тела: подпись считается по времени
// и пустому телу, как и для вебхуков.
func signMenuRequest(secret, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

var errMenuUnavailable = errors.New("меню заведения недоступно")

// reconcileSchedule — как часто идёт сверка. Раз в сутки ночью: чаще незачем,
// а реже — расхождение проживёт слишком долго.
const reconcileSchedule = "0 4 * * *"

// ScheduleReconciliation ставит сверку на расписание.
//
// Cron-workflow, а не внешний планировщик: расписание живёт рядом с процессом,
// переживает перезапуск воркера и видно в том же UI, что и остальные процессы.
// Повторный запуск идемпотентен — идентификатор постоянный.
func ScheduleReconciliation(
	ctx context.Context, temporal client.Client, taskQueue string, log *slog.Logger,
) error {
	_, err := temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    ReconciliationWorkflowID,
		TaskQueue:             taskQueue,
		CronSchedule:          reconcileSchedule,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}, MenuReconciliationWorkflow)
	if err != nil {
		if isAlreadyStarted(err) {
			return nil
		}

		return fmt.Errorf("расписание сверки меню: %w", err)
	}

	log.InfoContext(ctx, "сверка меню поставлена на расписание",
		slog.String("cron", reconcileSchedule))

	return nil
}
