package merchantapi

import (
	"github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	domain "github.com/emotionlessdev/avito-kitchen/internal/merchant"
)

// Перевод контракта в домен и обратно. Снапшот приходит с необязательными
// полями: там, где заведение промолчало, подставляются значения по умолчанию
// из спеки — домен работает с полными структурами и про указатели не знает.

func snapshot(request merchant.MenuSnapshot) domain.Snapshot {
	categories := make([]domain.SnapshotCategory, 0, len(request.Categories))
	for _, category := range request.Categories {
		categories = append(categories, domain.SnapshotCategory{
			ExternalID: category.ExternalId,
			Name:       category.Name,
			Position:   deref(category.Position, 0),
		})
	}

	items := make([]domain.SnapshotItem, 0, len(request.Items))
	for _, item := range request.Items {
		items = append(items, snapshotItem(item))
	}

	return domain.Snapshot{Categories: categories, Items: items}
}

func snapshotItem(item merchant.SnapshotItem) domain.SnapshotItem {
	converted := domain.SnapshotItem{
		ExternalID:         item.ExternalId,
		CategoryExternalID: item.CategoryExternalId,
		Name:               item.Name,
		Description:        deref(item.Description, ""),
		PriceMinor:         item.PriceMinor,
		WeightGrams:        item.WeightGrams,
		Position:           deref(item.Position, 0),
		IsAvailable:        deref(item.IsAvailable, true),
	}

	if item.OptionGroups == nil {
		return converted
	}

	for _, group := range *item.OptionGroups {
		converted.OptionGroups = append(converted.OptionGroups, snapshotGroup(group))
	}

	return converted
}

func snapshotGroup(group merchant.SnapshotOptionGroup) domain.SnapshotOptionGroup {
	converted := domain.SnapshotOptionGroup{
		ExternalID: group.ExternalId,
		Name:       group.Name,
		MinChoices: deref(group.MinChoices, 0),
		MaxChoices: deref(group.MaxChoices, 1),
		Position:   deref(group.Position, 0),
	}

	if group.Options == nil {
		return converted
	}

	for _, option := range *group.Options {
		converted.Options = append(converted.Options, domain.SnapshotOption{
			ExternalID:      option.ExternalId,
			Name:            option.Name,
			PriceDeltaMinor: deref(option.PriceDeltaMinor, 0),
			Position:        deref(option.Position, 0),
			IsAvailable:     deref(option.IsAvailable, true),
		})
	}

	return converted
}

func importResult(result domain.ImportResult) merchant.MenuImportResult {
	hash := result.PayloadHash

	return merchant.MenuImportResult{
		ImportId:    result.ImportID,
		Created:     result.Created,
		Updated:     result.Updated,
		Archived:    result.Archived,
		Idempotent:  result.Idempotent,
		PayloadHash: &hash,
	}
}

func availabilityRequest(request merchant.AvailabilityRequest) domain.AvailabilityRequest {
	return domain.AvailabilityRequest{
		Items:   availabilityChanges(request.Items),
		Options: availabilityChanges(request.Options),
	}
}

func availabilityChanges(changes *[]merchant.AvailabilityChange) []domain.AvailabilityChange {
	if changes == nil {
		return nil
	}

	converted := make([]domain.AvailabilityChange, 0, len(*changes))
	for _, change := range *changes {
		converted = append(converted, domain.AvailabilityChange{
			ExternalID:  change.ExternalId,
			IsAvailable: change.IsAvailable,
			Until:       change.Until,
		})
	}

	return converted
}

func availabilityResult(result domain.AvailabilityResult) merchant.AvailabilityResult {
	response := merchant.AvailabilityResult{
		ItemsUpdated:   result.ItemsUpdated,
		OptionsUpdated: result.OptionsUpdated,
	}

	if len(result.NotFound) > 0 {
		notFound := result.NotFound
		response.NotFound = &notFound
	}

	return response
}

func venueSettings(request merchant.VenueSettings) domain.VenueSettings {
	settings := domain.VenueSettings{
		IsAcceptingOrders:   request.IsAcceptingOrders,
		MinOrderAmountMinor: request.MinOrderAmountMinor,
		DeliveryFeeMinor:    request.DeliveryFeeMinor,
		DeliveryRadiusM:     request.DeliveryRadiusM,
		AvgPrepMinutes:      request.AvgPrepMinutes,
		Timezone:            request.Timezone,
	}

	if request.WorkHours == nil {
		return settings
	}

	// Пустой список — это «работать не будем», а не «не трогать расписание»:
	// различие несёт nil, поэтому непустой срез создаётся всегда.
	settings.WorkHours = make([]domain.WorkInterval, 0, len(*request.WorkHours))
	for _, interval := range *request.WorkHours {
		settings.WorkHours = append(settings.WorkHours, domain.WorkInterval{
			Weekday:  interval.Weekday,
			OpensAt:  interval.OpensAt,
			ClosesAt: interval.ClosesAt,
		})
	}

	return settings
}

func venueSettingsResponse(settings domain.VenueSettings) merchant.VenueSettings {
	response := merchant.VenueSettings{
		IsAcceptingOrders:   settings.IsAcceptingOrders,
		MinOrderAmountMinor: settings.MinOrderAmountMinor,
		DeliveryFeeMinor:    settings.DeliveryFeeMinor,
		DeliveryRadiusM:     settings.DeliveryRadiusM,
		AvgPrepMinutes:      settings.AvgPrepMinutes,
		Timezone:            settings.Timezone,
	}

	hours := make([]merchant.WorkHoursInterval, 0, len(settings.WorkHours))
	for _, interval := range settings.WorkHours {
		hours = append(hours, merchant.WorkHoursInterval{
			Weekday:  interval.Weekday,
			OpensAt:  interval.OpensAt,
			ClosesAt: interval.ClosesAt,
		})
	}

	response.WorkHours = &hours

	return response
}

func deref[T any](ptr *T, fallback T) T {
	if ptr == nil {
		return fallback
	}

	return *ptr
}
