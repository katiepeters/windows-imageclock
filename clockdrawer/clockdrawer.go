package clockdrawer

import (
	"fmt"
	"image"
	"image/color"
	"sync/atomic"
	"time"

	_ "embed"

	"github.com/fogleman/gg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

//go:embed fonts/Aileron-Regular.otf
var fontBytes []byte

type ClockDrawer struct {
	Name      string
	Format    string
	Big       bool
	StartTime time.Time
	x         int
	y         int
	face      font.Face
	color     color.NRGBA
	count     atomic.Uint64
}

func New(
	name string,
	color color.NRGBA,
	format string,
	big bool,
) (ClockDrawer, error) {
	if format != "jpeg" && format != "png" {
		return ClockDrawer{}, fmt.Errorf("unsupported format %s. supported formats: jpeg png", format)
	}

	multiple := 1
	if big {
		if format == "jpeg" {
			multiple = 4
		}
		if format == "png" {
			multiple = 4
		}
	}
	x := 2560 * multiple
	y := 1440 * multiple
	// create the fonet
	parsedFont, err := opentype.Parse(fontBytes)
	if err != nil {
		return ClockDrawer{}, fmt.Errorf("failed to parse font: %v", err)
	}

	face, err := opentype.NewFace(parsedFont, &opentype.FaceOptions{
		Size: float64(x / 30),
		DPI:  72,
	})

	if err != nil {
		return ClockDrawer{}, fmt.Errorf("failed to create new face: %v", err)
	}

	return ClockDrawer{
		x:         x,
		y:         y,
		Name:      name,
		face:      face,
		color:     color,
		Format:    format,
		Big:       big,
		StartTime: time.Now(),
	}, nil
}

func (cd *ClockDrawer) Ext() string {
	if cd.Format == "jpeg" {
		return ".jpg"
	}
	return ".png"
}

func (cd *ClockDrawer) Image(time string) (image.Image, error) {
	size := "small"
	if cd.Big {
		size = "big"
	}

	return NewImage(cd.x, cd.y, cd.color, []string{
		cd.Name,
		fmt.Sprintf("start_time: %d, size: %s, image_type: %s", cd.StartTime.Unix(), size, cd.Format),
		time,
		fmt.Sprintf("count: %d", cd.count.Add(1)),
	})
}

func NewImage(x, y int, col color.NRGBA, lines []string) (image.Image, error) {
	dc := gg.NewContext(x, y)
	parsedFont, err := opentype.Parse(fontBytes)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(parsedFont, &opentype.FaceOptions{
		Size: float64(x / 30),
		DPI:  72,
	})
	if err != nil {
		return nil, err
	}
	dc.SetFontFace(face)

	dc.SetRGBA255(int(col.R), int(col.G), int(col.B), int(col.A))
	for i, l := range lines {
		dc.DrawStringAnchored(l, float64(x/2), (float64(y)/float64(len(lines)+1))*float64(i+1), 0.5, 0.5)
	}

	return dc.Image(), nil
}
