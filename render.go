package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strings"
)

type excalidrawDocument struct {
	Type     string            `json:"type"`
	Version  int               `json:"version"`
	Elements []json.RawMessage `json:"elements"`
	AppState struct {
		ViewBackgroundColor string `json:"viewBackgroundColor"`
	} `json:"appState"`
}

type drawableElement struct {
	ID              string      `json:"id"`
	Type            string      `json:"type"`
	X               float64     `json:"x"`
	Y               float64     `json:"y"`
	Width           float64     `json:"width"`
	Height          float64     `json:"height"`
	BackgroundColor string      `json:"backgroundColor"`
	StrokeColor     string      `json:"strokeColor"`
	StrokeWidth     float64     `json:"strokeWidth"`
	Opacity         *float64    `json:"opacity"`
	IsDeleted       bool        `json:"isDeleted"`
	Text            string      `json:"text"`
	FontSize        float64     `json:"fontSize"`
	Points          [][]float64 `json:"points"`
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	available := b.limit - b.buffer.Len()
	if available <= 0 {
		b.exceeded = true
		return len(data), nil
	}
	if len(data) > available {
		_, _ = b.buffer.Write(data[:available])
		b.exceeded = true
		return len(data), nil
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}

func (b *limitedBuffer) WriteString(value string) (int, error) {
	return b.Write([]byte(value))
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

func renderSVG(document json.RawMessage) (template.HTML, error) {
	var doc excalidrawDocument
	if err := json.Unmarshal(document, &doc); err != nil {
		return "", err
	}
	if doc.Type != "excalidraw" || doc.Version != 2 {
		return "", fmt.Errorf("unsupported Excalidraw document")
	}
	elements := make([]drawableElement, 0, len(doc.Elements))
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, raw := range doc.Elements {
		var element drawableElement
		if err := json.Unmarshal(raw, &element); err != nil {
			return "", err
		}
		if element.IsDeleted {
			continue
		}
		elements = append(elements, element)
		minX = math.Min(minX, element.X)
		minY = math.Min(minY, element.Y)
		maxX = math.Max(maxX, element.X+math.Max(element.Width, 1))
		maxY = math.Max(maxY, element.Y+math.Max(element.Height, 1))
	}
	if len(elements) == 0 {
		minX, minY, maxX, maxY = 0, 0, 800, 600
	}
	const padding = 40.0
	viewX, viewY := minX-padding, minY-padding
	viewWidth, viewHeight := math.Max(maxX-minX+2*padding, 320), math.Max(maxY-minY+2*padding, 240)

	out := &limitedBuffer{limit: maxRenderedSVGBytes}
	fmt.Fprintf(out, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="%g %g %g %g" role="img" aria-label="Wireframe" preserveAspectRatio="xMidYMid meet">`, viewX, viewY, viewWidth, viewHeight)
	out.WriteString(`<defs><marker id="arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto"><polygon points="0 0,10 3.5,0 7" fill="#1e1e1e"/></marker></defs>`)
	fmt.Fprintf(out, `<rect x="%g" y="%g" width="%g" height="%g" fill="%s"/>`, viewX, viewY, viewWidth, viewHeight, safeColor(doc.AppState.ViewBackgroundColor, "#ffffff"))
	for _, element := range elements {
		fill := safeColor(element.BackgroundColor, "transparent")
		stroke := safeColor(element.StrokeColor, "#1e1e1e")
		strokeWidth := element.StrokeWidth
		if strokeWidth <= 0 {
			strokeWidth = 2
		}
		opacity := 1.0
		if element.Opacity != nil {
			opacity = math.Max(0, math.Min(*element.Opacity/100, 1))
		}
		id := template.HTMLEscapeString(element.ID)
		switch element.Type {
		case "rectangle":
			fmt.Fprintf(out, `<rect data-element-id="%s" x="%g" y="%g" width="%g" height="%g" rx="6" fill="%s" stroke="%s" stroke-width="%g" opacity="%g"/>`, id, element.X, element.Y, element.Width, element.Height, fill, stroke, strokeWidth, opacity)
		case "ellipse":
			fmt.Fprintf(out, `<ellipse data-element-id="%s" cx="%g" cy="%g" rx="%g" ry="%g" fill="%s" stroke="%s" stroke-width="%g" opacity="%g"/>`, id, element.X+element.Width/2, element.Y+element.Height/2, element.Width/2, element.Height/2, fill, stroke, strokeWidth, opacity)
		case "diamond":
			points := fmt.Sprintf("%g,%g %g,%g %g,%g %g,%g", element.X+element.Width/2, element.Y, element.X+element.Width, element.Y+element.Height/2, element.X+element.Width/2, element.Y+element.Height, element.X, element.Y+element.Height/2)
			fmt.Fprintf(out, `<polygon data-element-id="%s" points="%s" fill="%s" stroke="%s" stroke-width="%g" opacity="%g"/>`, id, points, fill, stroke, strokeWidth, opacity)
		case "text", "label", "arrowLabel":
			fontSize := element.FontSize
			if fontSize <= 0 {
				fontSize = 20
			}
			fmt.Fprintf(out, `<text data-element-id="%s" x="%g" y="%g" fill="%s" font-size="%g" font-family="Arial, sans-serif" opacity="%g">`, id, element.X, element.Y+fontSize, stroke, fontSize, opacity)
			for index, line := range strings.Split(element.Text, "\n") {
				dy := 0.0
				if index > 0 {
					dy = fontSize * 1.2
				}
				fmt.Fprintf(out, `<tspan x="%g" dy="%g">%s</tspan>`, element.X, dy, template.HTMLEscapeString(line))
			}
			out.WriteString(`</text>`)
		case "line", "arrow":
			points := element.Points
			if len(points) == 0 {
				points = [][]float64{{0, 0}, {element.Width, element.Height}}
			}
			out.WriteString(`<polyline data-element-id="` + id + `" points="`)
			for _, point := range points {
				if len(point) >= 2 {
					fmt.Fprintf(out, "%g,%g ", element.X+point[0], element.Y+point[1])
				}
			}
			fmt.Fprintf(out, `" fill="none" stroke="%s" stroke-width="%g" opacity="%g"`, stroke, strokeWidth, opacity)
			if element.Type == "arrow" {
				out.WriteString(` marker-end="url(#arrowhead)"`)
			}
			out.WriteString(`/>`)
		}
	}
	out.WriteString(`</svg>`)
	if out.exceeded {
		return "", fmt.Errorf("rendered SVG exceeds %d bytes", maxRenderedSVGBytes)
	}
	return template.HTML(out.String()), nil
}

func safeColor(value, fallback string) string {
	if len(value) == 4 || len(value) == 7 {
		if value[0] == '#' {
			for _, r := range value[1:] {
				if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
					return fallback
				}
			}
			return value
		}
	}
	if value == "transparent" {
		return value
	}
	return fallback
}
