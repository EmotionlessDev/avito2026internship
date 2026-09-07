package publicapi

import (
	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/catalog"
	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/search"
)

// Перевод доменных типов в типы контракта. Отдельный файл, потому что это
// единственное место, где домен знает про форму ответа: сервисы про HTTP
// не знают вовсе.

func venueSummary(view catalog.VenueView) public.VenueSummary {
	return public.VenueSummary{
		Id:                  view.ID,
		Slug:                view.Slug,
		Name:                view.Name,
		AddressText:         view.AddressText,
		Lat:                 view.Point.Lat,
		Lon:                 view.Point.Lon,
		Timezone:            view.Timezone,
		IsOpenNow:           view.IsOpenNow,
		NextOpenAt:          view.NextOpenAt,
		IsAcceptingOrders:   view.IsAcceptingOrders,
		MinOrderAmountMinor: view.MinOrderAmountMinor,
		DeliveryFeeMinor:    view.DeliveryFeeMinor,
		DeliveryRadiusM:     view.DeliveryRadiusM,
		AvgPrepMinutes:      view.AvgPrepMinutes,
		DistanceM:           view.DistanceM,
	}
}

func venueDetails(view catalog.VenueView) public.VenueDetails {
	summary := venueSummary(view)

	hours := make([]public.WorkHoursInterval, 0, len(view.Schedule.Intervals))
	for _, interval := range view.Schedule.Intervals {
		hours = append(hours, public.WorkHoursInterval{
			Weekday:  int(interval.Weekday),
			OpensAt:  interval.Opens.String(),
			ClosesAt: interval.Closes.String(),
		})
	}

	return public.VenueDetails{
		Id:                  summary.Id,
		Slug:                summary.Slug,
		Name:                summary.Name,
		AddressText:         summary.AddressText,
		Lat:                 summary.Lat,
		Lon:                 summary.Lon,
		Timezone:            summary.Timezone,
		IsOpenNow:           summary.IsOpenNow,
		NextOpenAt:          summary.NextOpenAt,
		IsAcceptingOrders:   summary.IsAcceptingOrders,
		MinOrderAmountMinor: summary.MinOrderAmountMinor,
		DeliveryFeeMinor:    summary.DeliveryFeeMinor,
		DeliveryRadiusM:     summary.DeliveryRadiusM,
		AvgPrepMinutes:      summary.AvgPrepMinutes,
		DistanceM:           summary.DistanceM,
		WorkHours:           hours,
	}
}

func menu(domain catalog.Menu) public.Menu {
	categories := make([]public.MenuCategory, 0, len(domain.Categories))

	for _, category := range domain.Categories {
		items := make([]public.MenuItem, 0, len(category.Items))

		for _, item := range category.Items {
			items = append(items, menuItem(item))
		}

		categories = append(categories, public.MenuCategory{
			Id:         category.ID,
			ExternalId: category.ExternalID,
			Name:       category.Name,
			Position:   category.Position,
			Items:      items,
		})
	}

	return public.Menu{VenueId: domain.VenueID, Categories: categories}
}

func menuItem(item catalog.Item) public.MenuItem {
	groups := make([]public.OptionGroup, 0, len(item.OptionGroups))

	for _, group := range item.OptionGroups {
		options := make([]public.Option, 0, len(group.Options))

		for _, option := range group.Options {
			options = append(options, public.Option{
				Id:              option.ID,
				ExternalId:      option.ExternalID,
				Name:            option.Name,
				PriceDeltaMinor: option.PriceDeltaMinor,
				IsAvailable:     option.IsAvailable,
			})
		}

		groups = append(groups, public.OptionGroup{
			Id:         group.ID,
			ExternalId: group.ExternalID,
			Name:       group.Name,
			MinChoices: group.MinChoices,
			MaxChoices: group.MaxChoices,
			Options:    options,
		})
	}

	result := public.MenuItem{
		Id:           item.ID,
		ExternalId:   item.ExternalID,
		Name:         item.Name,
		PriceMinor:   item.PriceMinor,
		WeightGrams:  item.WeightGrams,
		IsAvailable:  item.IsAvailable,
		OptionGroups: groups,
	}

	// Пустое описание не отдаём полем со значением "": в контракте оно
	// необязательное, и его отсутствие честнее пустой строки.
	if item.Description != "" {
		description := item.Description
		result.Description = &description
	}

	return result
}

func searchItem(result search.Result) public.SearchItem {
	return public.SearchItem{
		ItemId:         result.ItemID,
		VenueId:        result.VenueID,
		ItemName:       result.ItemName,
		VenueName:      result.VenueName,
		CategoryName:   result.CategoryName,
		PriceMinor:     result.PriceMinor,
		IsAvailable:    result.IsAvailable,
		VenueIsOpenNow: result.VenueIsOpenNow,
		DistanceM:      result.DistanceM,
	}
}

// nextCursor переводит пустую строку в отсутствующее поле: «страниц больше нет»
// в контракте выражено как null, а не пустой строкой.
func nextCursor(cursor string) *string {
	if cursor == "" {
		return nil
	}

	return &cursor
}

func cartResponse(current cart.Cart) public.Cart {
	lines := make([]public.CartItem, 0, len(current.Lines))
	for i := range current.Lines {
		lines = append(lines, cartLine(current.Lines[i]))
	}

	response := public.Cart{
		Id:               current.ID,
		VenueId:          current.VenueID,
		Items:            lines,
		ItemsAmountMinor: current.ItemsAmountMinor(),
		UpdatedAt:        &current.UpdatedAt,
	}

	if current.VenueName != "" {
		name := current.VenueName
		response.VenueName = &name
	}

	return response
}

func cartLine(line cart.Line) public.CartItem {
	options := make([]public.SelectedOption, 0, len(line.Options))
	for i := range line.Options {
		option := line.Options[i]
		available := option.IsAvailable
		groupID := option.GroupID

		options = append(options, public.SelectedOption{
			OptionId:        option.OptionID,
			GroupId:         &groupID,
			Name:            option.Name,
			PriceDeltaMinor: option.PriceDeltaMinor,
			IsAvailable:     &available,
		})
	}

	return public.CartItem{
		Id:             line.ID,
		MenuItemId:     line.MenuItemID,
		Name:           line.Name,
		Qty:            line.Qty,
		UnitPriceMinor: line.UnitPriceMinor,
		TotalMinor:     line.TotalMinor(),
		IsAvailable:    line.IsAvailable,
		Options:        options,
	}
}

func validationResponse(result cart.Result) public.CartValidation {
	issues := make([]public.CartIssue, 0, len(result.Issues))
	for i := range result.Issues {
		issues = append(issues, cartIssue(result.Issues[i]))
	}

	return public.CartValidation{
		IsValid:          result.IsValid,
		VenueId:          result.VenueID,
		ItemsAmountMinor: result.ItemsAmountMinor,
		DeliveryFeeMinor: &result.DeliveryFeeMinor,
		TotalAmountMinor: result.TotalAmountMinor,
		Issues:           issues,
	}
}

func cartIssue(issue cart.Issue) public.CartIssue {
	converted := public.CartIssue{
		Code:            public.CartIssueCode(issue.Code),
		Severity:        public.IssueSeverity(issue.Severity),
		ItemId:          issue.ItemID,
		OptionId:        issue.OptionID,
		OldPriceMinor:   issue.OldPriceMinor,
		NewPriceMinor:   issue.NewPriceMinor,
		MinOrderMinor:   issue.MinOrderMinor,
		MissingMinor:    issue.MissingMinor,
		NextOpenAt:      issue.NextOpenAt,
		DistanceM:       issue.DistanceM,
		DeliveryRadiusM: issue.DeliveryRadiusM,
	}

	if issue.Message != "" {
		message := issue.Message
		converted.Message = &message
	}

	if issue.ItemName != "" {
		name := issue.ItemName
		converted.ItemName = &name
	}

	if issue.OptionName != "" {
		name := issue.OptionName
		converted.OptionName = &name
	}

	return converted
}
