package types

import (
	"bytes"
	"crypto/md5"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"

	"github.com/gotd/td/tg"
)

type File struct {
	Location tg.InputFileLocationClass
	FileSize int64
	FileName string
	MimeType string
	ID       int64
}

// fileGob is a helper struct for gob encoding/decoding
// It stores the concrete type name and data separately
type fileGob struct {
	LocationType string // "document" or "photo"
	LocationData []byte
	FileSize     int64
	FileName     string
	MimeType     string
	ID           int64
}

// GobEncode implements gob.GobEncoder
func (f *File) GobEncode() ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)

	fg := fileGob{
		FileSize: f.FileSize,
		FileName: f.FileName,
		MimeType: f.MimeType,
		ID:       f.ID,
	}

	// Encode the Location based on its concrete type
	switch loc := f.Location.(type) {
	case *tg.InputDocumentFileLocation:
		fg.LocationType = "document"
		var locBuf bytes.Buffer
		locEnc := gob.NewEncoder(&locBuf)
		if err := locEnc.Encode(loc); err != nil {
			return nil, err
		}
		fg.LocationData = locBuf.Bytes()
	case *tg.InputPhotoFileLocation:
		fg.LocationType = "photo"
		var locBuf bytes.Buffer
		locEnc := gob.NewEncoder(&locBuf)
		if err := locEnc.Encode(loc); err != nil {
			return nil, err
		}
		fg.LocationData = locBuf.Bytes()
	default:
		return nil, fmt.Errorf("unsupported location type: %T", f.Location)
	}

	if err := enc.Encode(fg); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// GobDecode implements gob.GobDecoder
func (f *File) GobDecode(data []byte) error {
	var fg fileGob
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)

	if err := dec.Decode(&fg); err != nil {
		return err
	}

	f.FileSize = fg.FileSize
	f.FileName = fg.FileName
	f.MimeType = fg.MimeType
	f.ID = fg.ID

	// Decode the Location based on the stored type
	locBuf := bytes.NewBuffer(fg.LocationData)
	locDec := gob.NewDecoder(locBuf)

	switch fg.LocationType {
	case "document":
		var loc tg.InputDocumentFileLocation
		if err := locDec.Decode(&loc); err != nil {
			return err
		}
		f.Location = &loc
	case "photo":
		var loc tg.InputPhotoFileLocation
		if err := locDec.Decode(&loc); err != nil {
			return err
		}
		f.Location = &loc
	default:
		return fmt.Errorf("unknown location type: %s", fg.LocationType)
	}

	return nil
}

type HashableFileStruct struct {
	FileName string
	FileSize int64
	MimeType string
	FileID   int64
}

func (f *HashableFileStruct) Pack() string {
	hasher := md5.New()
	val := reflect.ValueOf(*f)
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)

		var fieldValue []byte
		switch field.Kind() {
		case reflect.String:
			fieldValue = []byte(field.String())
		case reflect.Int64:
			fieldValue = []byte(strconv.FormatInt(field.Int(), 10))
		}

		hasher.Write(fieldValue)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
