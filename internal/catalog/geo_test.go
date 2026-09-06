package catalog_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/catalog"

	"github.com/stretchr/testify/assert"
)

func TestDistanceM(t *testing.T) {
	t.Parallel()

	// Точки взяты из сида: заведения и адрес доставки демо-сценариев.
	hachapuri := catalog.Point{Lat: 55.7402, Lon: 37.6280}
	supra := catalog.Point{Lat: 55.7048, Lon: 37.5720}

	tests := map[string]struct {
		a, b      catalog.Point
		wantM     int
		tolerance int
	}{
		"точка сама с собой": {
			a: hachapuri, b: hachapuri, wantM: 0, tolerance: 0,
		},
		"два заведения сида": {
			// Сверено плоской аппроксимацией: 0.0354° широты — 3.94 км,
			// 0.056° долготы на широте 55.7° — 3.51 км, гипотенуза — 5.27 км.
			a: hachapuri, b: supra, wantM: 5272, tolerance: 30,
		},
		"расстояние симметрично": {
			a: supra, b: hachapuri, wantM: 5272, tolerance: 30,
		},
		"градус широты — около 111 км": {
			a: catalog.Point{Lat: 55, Lon: 37}, b: catalog.Point{Lat: 56, Lon: 37},
			wantM: 111195, tolerance: 500,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.wantM, catalog.DistanceM(tt.a, tt.b), float64(tt.tolerance))
		})
	}
}
