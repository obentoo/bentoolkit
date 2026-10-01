// Package sni exports a StatusNotifierItem (the freedesktop/KDE system tray
// protocol) over D-Bus.
package sni

// Pixmap is an SNI `(iiay)` pixmap: a Width x Height image whose Data holds
// one ARGB32 pixel per four bytes, in A,R,G,B order (network byte order), row
// by row, not premultiplied — len(Data) is Width*Height*4.
type Pixmap struct {
	Width, Height int32
	Data          []byte
}
