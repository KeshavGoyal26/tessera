// Package types holds small value types shared by the SFU engine packages
// (rtc, buffer, simulcast, bwe, speaker) without import cycles.
package types

import tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"

// Layer limits.
const (
	MaxSpatialLayers  = 3
	MaxTemporalLayers = 4
	// InvalidLayer marks "no layer" for Spatial or Temporal.
	InvalidLayer int32 = -1
)

// VideoLayer is a (spatial, temporal) layer pair. Spatial 0..2 maps to the
// simulcast RIDs q, h, f.
type VideoLayer struct {
	Spatial  int32
	Temporal int32
}

// InvalidVideoLayer is the zero-value "no layer selected".
var InvalidVideoLayer = VideoLayer{Spatial: InvalidLayer, Temporal: InvalidLayer}

// IsValid reports whether both components are set.
func (l VideoLayer) IsValid() bool { return l.Spatial != InvalidLayer && l.Temporal != InvalidLayer }

// GreaterThan compares spatial first, then temporal.
func (l VideoLayer) GreaterThan(o VideoLayer) bool {
	return l.Spatial > o.Spatial || (l.Spatial == o.Spatial && l.Temporal > o.Temporal)
}

// RIDs in spatial order.
var RIDs = [MaxSpatialLayers]string{"q", "h", "f"}

// SpatialFromRID returns the spatial index for a RID, or InvalidLayer.
// A track without RID (no simulcast) is layer 0.
func SpatialFromRID(rid string) int32 {
	switch rid {
	case "", "q":
		return 0
	case "h":
		return 1
	case "f":
		return 2
	}
	return InvalidLayer
}

// QualityForSpatial maps a spatial index to the protobuf quality.
func QualityForSpatial(s int32) tesserav1.VideoQuality {
	switch s {
	case 0:
		return tesserav1.VideoQuality_VIDEO_QUALITY_LOW
	case 1:
		return tesserav1.VideoQuality_VIDEO_QUALITY_MEDIUM
	case 2:
		return tesserav1.VideoQuality_VIDEO_QUALITY_HIGH
	}
	return tesserav1.VideoQuality_VIDEO_QUALITY_OFF
}

// SpatialForQuality maps a protobuf quality to a spatial index.
func SpatialForQuality(q tesserav1.VideoQuality) int32 {
	switch q {
	case tesserav1.VideoQuality_VIDEO_QUALITY_LOW:
		return 0
	case tesserav1.VideoQuality_VIDEO_QUALITY_MEDIUM:
		return 1
	case tesserav1.VideoQuality_VIDEO_QUALITY_HIGH:
		return 2
	}
	return InvalidLayer
}

// Bitrates are measured bitrates in bits per second per (spatial, temporal) layer,
// cumulative over temporal layers of the same spatial layer.
type Bitrates [MaxSpatialLayers][MaxTemporalLayers]int64
