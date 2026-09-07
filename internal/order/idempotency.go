package order

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Fingerprint — отпечаток запроса на оформление.
//
// Считается по тем полям, которые определяют заказ со стороны клиента:
// куда везти, кому звонить, что он подтвердил. Состав корзины в отпечаток
// не входит намеренно — между ретраями сети он не меняется, а если поменялся,
// это уже другой заказ и другой ключ.
func Fingerprint(request CreateRequest) string {
	// Явная конкатенация с разделителем, а не JSON: формат отпечатка
	// не должен меняться от того, как когда-нибудь переименуют поля структуры.
	raw := fmt.Sprintf("%s\x1f%.6f\x1f%.6f\x1f%s\x1f%s\x1f%t",
		request.Delivery.Address,
		request.Delivery.Point.Lat,
		request.Delivery.Point.Lon,
		request.ContactPhone,
		request.Comment,
		request.AcceptPriceChanges,
	)

	sum := sha256.Sum256([]byte(raw))

	return hex.EncodeToString(sum[:])
}
