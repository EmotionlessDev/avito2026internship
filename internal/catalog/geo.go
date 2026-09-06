package catalog

import "math"

// Point — географическая точка: координаты заведения или адреса доставки.
type Point struct {
	Lat float64
	Lon float64
}

// earthRadiusM — средний радиус Земли. Для зоны доставки в километрах
// сферической модели достаточно: ошибка эллипсоида здесь меньше, чем
// погрешность самой идеи «зона доставки — круг».
const earthRadiusM = 6371000.0

// DistanceM возвращает расстояние между точками по большому кругу
// (формула гаверсинуса), в метрах.
//
// Зона доставки в MVP — радиус, а не полигон (docs/DESIGN.md §11), поэтому
// это единственная геометрия, которая нужна: сравнить расстояние с
// delivery_radius_m и показать пользователю, насколько он далеко.
func DistanceM(a, b Point) int {
	lat1 := degreesToRadians(a.Lat)
	lat2 := degreesToRadians(b.Lat)
	deltaLat := degreesToRadians(b.Lat - a.Lat)
	deltaLon := degreesToRadians(b.Lon - a.Lon)

	h := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(deltaLon/2)*math.Sin(deltaLon/2)

	return int(math.Round(2 * earthRadiusM * math.Asin(math.Sqrt(h))))
}

func degreesToRadians(deg float64) float64 {
	return deg * math.Pi / 180
}
