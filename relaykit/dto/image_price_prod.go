package dto

import "strings"

// imagePriceRatio adds the prod-only Imagen tier on top of upstream's dall-e table:
// quality maps to imageSize (1K default, 2K for hd/high), and Google prices 2K at
// about 1.5x the 1K rate.
func (i *ImageRequest) imagePriceRatio() float64 {
	if strings.HasPrefix(i.Model, "imagen") {
		switch i.Quality {
		case "hd", "high", "2K":
			return 1.5
		}
		return 1
	}
	return i.legacyDallePriceRatio()
}
