package main

// HEIF dimensions live in ISO-BMFF item properties, independently of EXIF.
// Read only bounded metadata boxes; never allocate/read the compressed media payload.
import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

type box struct {
	kind         string
	payload, end int64
}

func boxes(r io.ReaderAt, start, end int64) ([]box, error) {
	var result []box
	for pos := start; pos < end; {
		if end-pos < 8 || len(result) > 100000 {
			return nil, fmt.Errorf("invalid HEIF box layout")
		}
		var header [16]byte
		if _, err := r.ReadAt(header[:8], pos); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		headerSize := int64(8)
		if size == 1 {
			if _, err := r.ReadAt(header[8:], pos+8); err != nil {
				return nil, err
			}
			n := binary.BigEndian.Uint64(header[8:])
			if n > uint64(end-pos) {
				return nil, fmt.Errorf("oversized HEIF box")
			}
			size = int64(n)
			headerSize = 16
		}
		if size == 0 {
			size = end - pos
		}
		if size < headerSize || size > end-pos {
			return nil, fmt.Errorf("invalid HEIF box size")
		}
		result = append(result, box{string(header[4:8]), pos + headerSize, pos + size})
		pos += size
	}
	return result, nil
}
func boxData(r io.ReaderAt, b box, max int64) ([]byte, error) {
	if b.end-b.payload > max {
		return nil, fmt.Errorf("HEIF metadata box too large")
	}
	data := make([]byte, b.end-b.payload)
	_, err := r.ReadAt(data, b.payload)
	return data, err
}

type heifProperty struct {
	width, height, rotation int
	rotates                 bool
}

func heifDimensions(f *os.File) (int, int, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	top, err := boxes(f, 0, info.Size())
	if err != nil {
		return 0, 0, err
	}
	for _, meta := range top {
		if meta.kind != "meta" {
			continue
		}
		children, err := boxes(f, meta.payload+4, meta.end)
		if err != nil {
			return 0, 0, err
		}
		var primary uint32
		properties := []heifProperty{{}}
		associations := map[uint32][]int{}
		for _, child := range children {
			switch child.kind {
			case "pitm":
				data, e := boxData(f, child, 16)
				if e != nil || len(data) < 6 {
					return 0, 0, fmt.Errorf("invalid HEIF primary item")
				}
				if data[0] == 0 {
					primary = uint32(binary.BigEndian.Uint16(data[4:6]))
				} else if len(data) >= 8 {
					primary = binary.BigEndian.Uint32(data[4:8])
				}
			case "iprp":
				items, e := boxes(f, child.payload, child.end)
				if e != nil {
					return 0, 0, e
				}
				for _, item := range items {
					switch item.kind {
					case "ipco":
						props, e := boxes(f, item.payload, item.end)
						if e != nil {
							return 0, 0, e
						}
						for _, prop := range props {
							p := heifProperty{}
							if prop.kind == "ispe" {
								data, e := boxData(f, prop, 12)
								if e != nil || len(data) != 12 {
									return 0, 0, fmt.Errorf("invalid HEIF spatial extent")
								}
								p.width = int(binary.BigEndian.Uint32(data[4:8]))
								p.height = int(binary.BigEndian.Uint32(data[8:12]))
							}
							if prop.kind == "irot" {
								data, e := boxData(f, prop, 1)
								if e != nil || len(data) != 1 {
									return 0, 0, fmt.Errorf("invalid HEIF rotation")
								}
								p.rotation = int(data[0] & 3)
								p.rotates = true
							}
							properties = append(properties, p)
						}
					case "ipma":
						data, e := boxData(f, item, 8<<20)
						if e != nil || len(data) < 8 {
							return 0, 0, fmt.Errorf("invalid HEIF associations")
						}
						version, wide := data[0], data[3]&1 != 0
						entries := binary.BigEndian.Uint32(data[4:8])
						offset := 8
						for entry := uint32(0); entry < entries; entry++ {
							idSize := 2
							if version >= 1 {
								idSize = 4
							}
							if offset+idSize+1 > len(data) {
								return 0, 0, io.ErrUnexpectedEOF
							}
							id := uint32(binary.BigEndian.Uint16(data[offset:]))
							if idSize == 4 {
								id = binary.BigEndian.Uint32(data[offset:])
							}
							offset += idSize
							count := int(data[offset])
							offset++
							for range count {
								size := 1
								if wide {
									size = 2
								}
								if offset+size > len(data) {
									return 0, 0, io.ErrUnexpectedEOF
								}
								index := int(data[offset] & 0x7f)
								if wide {
									index = int(binary.BigEndian.Uint16(data[offset:]) & 0x7fff)
								}
								offset += size
								associations[id] = append(associations[id], index)
							}
						}
					}
				}
			}
		}
		width, height, rotation := 0, 0, 0
		for _, index := range associations[primary] {
			if index <= 0 || index >= len(properties) {
				continue
			}
			p := properties[index]
			if p.width > 0 && p.height > 0 {
				width, height = p.width, p.height
			}
			if p.rotates {
				rotation = p.rotation
			}
		}
		if width > 0 && height > 0 {
			if rotation%2 == 1 {
				width, height = height, width
			}
			return width, height, nil
		}
	}
	return 0, 0, fmt.Errorf("HEIF primary image dimensions not found")
}

// HEIF uses item locations (iloc), including 32-bit item IDs in version 2.
// Some older readers only handle 16-bit IDs. Resolve local-file/idat extents here.
type heifCursor struct {
	data   []byte
	offset int
	err    error
}

func (c *heifCursor) number(n int) uint64 {
	if c.err != nil {
		return 0
	}
	if n < 0 || n > 8 || c.offset+n > len(c.data) {
		c.err = io.ErrUnexpectedEOF
		return 0
	}
	var value uint64
	for _, b := range c.data[c.offset : c.offset+n] {
		value = value<<8 | uint64(b)
	}
	c.offset += n
	return value
}
func heifExif(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	top, err := boxes(f, 0, info.Size())
	if err != nil {
		return nil, err
	}
	for _, meta := range top {
		if meta.kind != "meta" {
			continue
		}
		children, err := boxes(f, meta.payload+4, meta.end)
		if err != nil {
			return nil, err
		}
		var exifID uint64
		var location box
		var idatOffset int64
		for _, child := range children {
			switch child.kind {
			case "idat":
				idatOffset = child.payload
			case "iloc":
				location = child
			case "iinf":
				var version [1]byte
				if _, err = f.ReadAt(version[:], child.payload); err != nil {
					return nil, err
				}
				skip := int64(6)
				if version[0] != 0 {
					skip = 8
				}
				items, err := boxes(f, child.payload+skip, child.end)
				if err != nil {
					return nil, err
				}
				for _, item := range items {
					if item.kind != "infe" {
						continue
					}
					data, err := boxData(f, item, 1<<20)
					if err != nil {
						return nil, err
					}
					if len(data) < 12 {
						continue
					}
					if data[0] == 2 && string(data[8:12]) == "Exif" {
						exifID = uint64(binary.BigEndian.Uint16(data[4:6]))
					}
					if data[0] == 3 && len(data) >= 14 && string(data[10:14]) == "Exif" {
						exifID = uint64(binary.BigEndian.Uint32(data[4:8]))
					}
				}
			}
		}
		if exifID == 0 || location.kind == "" {
			continue
		}
		data, err := boxData(f, location, 8<<20)
		if err != nil || len(data) < 8 {
			return nil, fmt.Errorf("invalid HEIF item locations")
		}
		version := data[0]
		if version > 2 {
			return nil, fmt.Errorf("unsupported HEIF iloc version")
		}
		offsetSize, lengthSize, baseSize, indexSize := int(data[4]>>4), int(data[4]&15), int(data[5]>>4), int(data[5]&15)
		cursor := heifCursor{data: data, offset: 6}
		idSize := 2
		if version == 2 {
			idSize = 4
		}
		count := cursor.number(idSize)
		if count > 100000 {
			return nil, fmt.Errorf("too many HEIF items")
		}
		for entry := uint64(0); entry < count; entry++ {
			id := cursor.number(idSize)
			method := uint64(0)
			if version > 0 {
				method = cursor.number(2) & 15
			}
			reference := cursor.number(2)
			base := cursor.number(baseSize)
			extents := cursor.number(2)
			var result []byte
			for extent := uint64(0); extent < extents; extent++ {
				if version > 0 && indexSize > 0 {
					cursor.number(indexSize)
				}
				offset, length := cursor.number(offsetSize), cursor.number(lengthSize)
				if cursor.err != nil {
					return nil, cursor.err
				}
				if id != exifID {
					continue
				}
				if reference != 0 || method > 1 {
					return nil, fmt.Errorf("nonlocal HEIF EXIF item")
				}
				origin := uint64(0)
				if method == 1 {
					if idatOffset == 0 {
						return nil, fmt.Errorf("missing HEIF idat")
					}
					origin = uint64(idatOffset)
				}
				fileSize := uint64(info.Size())
				if length > 4<<20 || uint64(len(result))+length > 4<<20 || base > fileSize || offset > fileSize-base || origin > fileSize-base-offset || length > fileSize-base-offset-origin {
					return nil, fmt.Errorf("invalid HEIF EXIF extent")
				}
				start := len(result)
				result = append(result, make([]byte, int(length))...)
				if _, err = f.ReadAt(result[start:], int64(origin+base+offset)); err != nil {
					return nil, err
				}
			}
			if cursor.err != nil {
				return nil, cursor.err
			}
			if id == exifID {
				if len(result) < 4 {
					return nil, io.ErrUnexpectedEOF
				}
				start := uint64(binary.BigEndian.Uint32(result[:4])) + 4
				if start >= uint64(len(result)) {
					return nil, fmt.Errorf("invalid HEIF TIFF offset")
				}
				return result[start:], nil
			}
		}
	}
	return nil, fmt.Errorf("HEIF EXIF item not found")
}
