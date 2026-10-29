package experiment

import (
	"errors"
	"fmt"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
)

type Series struct {
	Name string
	X, Y []float64
}

func LineChart(path, title, xLabel, yLabel string, series []Series) error {
	p := plot.New()
	p.Title.Text, p.X.Label.Text, p.Y.Label.Text = title, xLabel, yLabel
	drawn := 0
	for _, s := range series {
		if len(s.X) != len(s.Y) {
			return fmt.Errorf("series %q has %d x values and %d y values", s.Name, len(s.X), len(s.Y))
		}
		if len(s.X) == 0 {
			continue
		}
		pts := make(plotter.XYs, len(s.X))
		for i := range pts {
			pts[i].X, pts[i].Y = s.X[i], s.Y[i]
		}
		line, err := plotter.NewLine(pts)
		if err != nil {
			return fmt.Errorf("series %q: %w", s.Name, err)
		}
		line.Color = palette[drawn%len(palette)]
		p.Add(line)
		p.Legend.Add(s.Name, line)
		drawn++
	}
	if drawn == 0 {
		return errors.New("chart has no data")
	}
	if err := p.Save(10*vg.Inch, 4*vg.Inch, path); err != nil {
		return fmt.Errorf("save chart %s: %w", path, err)
	}
	return nil
}
