package gofigure

import (
	"errors"
	"log"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/ian-kent/gofigure/sources"
)

// Debug controls log output
var Debug = false
var _ = func() {
	sources.Debug = Debug
	sources.Logger = printf
}

func printf(message string, args ...interface{}) {
	if !Debug {
		return
	}
	log.Printf(message, args...)
}

/* TODO
 * - Add file/http sources
 *   - Add "decoders", e.g. json/env/xml
 * - Default value (if gofigure is func()*StructType)
 * - Ignore lowercased "unexported" fields?
 */

// Gofiguration represents a parsed struct
type Gofiguration struct {
	order   []string
	params  map[string]map[string]string
	fields  map[string]*Gofiguritem
	flagged bool
	s       interface{}
}

func (gfg *Gofiguration) printf(message string, args ...interface{}) {
	printf(message, args...)
}

// Gofiguritem represents a single struct field
type Gofiguritem struct {
	keys    map[string]string
	field   string
	goField reflect.StructField
	goValue reflect.Value
}

// Sources contains a map of struct field tag names to source implementation
var Sources = map[string]sources.Source{
	"env":  &sources.Environment{},
	"flag": &sources.CommandLine{},
}

// DefaultOrder sets the default order used
var DefaultOrder = []string{"env", "flag"}

var (
	// ReEnvPrefix is used to restrict envPrefix config values
	ReEnvPrefix = regexp.MustCompile("^([A-Z][A-Z0-9_]+|)$")
)

var (
	// ErrInvalidOrder is returned if the "order" struct tag is invalid
	ErrInvalidOrder = errors.New("Invalid order")
	// ErrUnsupportedFieldType is returned for unsupported field types,
	// e.g. chan or func
	ErrUnsupportedFieldType = errors.New("Unsupported field type")
	// ErrInvalidEnvPrefix is returned if the value of envPrefix doesn't
	// match ReEnvPrefix
	ErrInvalidEnvPrefix = errors.New("Invalid environment variable name prefix")
)

// ParseStruct creates a Gofiguration from a struct
func ParseStruct(s interface{}) (*Gofiguration, error) {
	t := reflect.TypeOf(s).Elem()
	v := reflect.ValueOf(s).Elem()

	gfg := &Gofiguration{
		params: make(map[string]map[string]string),
		order:  DefaultOrder,
		fields: make(map[string]*Gofiguritem),
		s:      s,
	}

	err := gfg.parseGofigureField(t)
	if err != nil {
		return nil, err
	}

	gfg.parseFields(v, t)

	return gfg, nil
}

func getStructTags(tag string) map[string]string {
	// http://golang.org/src/pkg/reflect/type.go?s=20885:20906#L747
	m := make(map[string]string)
	for tag != "" {
		// skip leading space
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}

		// scan to colon.
		// a space or a quote is a syntax error
		i = 0
		for i < len(tag) && tag[i] != ' ' && tag[i] != ':' && tag[i] != '"' {
			i++
		}
		if i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		name := string(tag[:i])
		tag = tag[i+1:]

		// scan quoted string to find value
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		qvalue := string(tag[:i+1])
		tag = tag[i+1:]

		value, _ := strconv.Unquote(qvalue)
		m[name] = value
	}
	return m
}

var argRe = regexp.MustCompile("([a-z]+)([A-Z][a-z]+)")

func (gfg *Gofiguration) parseGofigureField(t reflect.Type) error {
	gf, ok := t.FieldByName("gofigure")
	if ok {
		tags := getStructTags(string(gf.Tag))
		for name, value := range tags {
			if name == "order" {
				oParts := strings.Split(value, ",")
				for _, p := range oParts {
					if _, ok := Sources[p]; !ok {
						return ErrInvalidOrder
					}
				}
				gfg.order = oParts
				continue
			}
			// Parse orderKey:"value" tags, e.g.
			// envPrefix, which gets split into
			//   gfg.params["env"]["prefix"] = "value"
			// gfg.params["env"] is then passed to
			// source registered with that key
			match := argRe.FindStringSubmatch(name)
			if len(match) > 1 {
				if _, ok := gfg.params[match[1]]; !ok {
					gfg.params[match[1]] = make(map[string]string)
				}
				gfg.params[match[1]][strings.ToLower(match[2])] = value
			}
		}
	}
	return nil
}

func (gfg *Gofiguration) parseFields(v reflect.Value, t reflect.Type) {
	gfg.printf("Found %d fields", t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i).Name
		if f == "gofigure" {
			gfg.printf("Skipped field '%s'", f)
			continue
		}

		gfg.printf("Parsed field '%s'", f)

		gfi := &Gofiguritem{
			field:   f,
			goField: t.Field(i),
			goValue: v.Field(i),
			keys:    make(map[string]string),
		}
		tag := t.Field(i).Tag
		if len(tag) > 0 {
			gfi.keys = getStructTags(string(tag))
		}
		gfg.fields[f] = gfi
	}
}

func (gfg *Gofiguration) cleanupSources() {
	for _, o := range gfg.order {
		Sources[o].Cleanup()
	}
}

func (gfg *Gofiguration) initSources() error {
	for _, o := range gfg.order {
		err := Sources[o].Init(gfg.params[o])
		if err != nil {
			return err
		}
	}
	return nil
}

func (gfg *Gofiguration) registerFields() error {
	for _, gfi := range gfg.fields {
		for _, o := range gfg.order {
			kn := gfi.field
			if k, ok := gfi.keys[o]; ok {
				kn = k
			}
			gfg.printf("Registering '%s' for source '%s' with key '%s'", gfi.field, o, kn)
			err := Sources[o].Register(kn, "", gfi.keys, gfi.goField.Type)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func numVal(i string) string {
	if len(i) == 0 {
		return "0"
	}
	return i
}

func (gfi *Gofiguritem) populateDefaultType(order []string) error {
	var prevVal *string

	for _, source := range order {
		kn := gfi.field
		if k, ok := gfi.keys[source]; ok {
			kn = k
		}

		val, err := Sources[source].Get(kn, prevVal)
		if err != nil {
			return err
		}

		prevVal = &val

		printf("Got value '%s' from source '%s' for key '%s'", val, source, gfi.field)

		switch gfi.goField.Type.Kind() {
		case reflect.Bool:
			if len(val) == 0 {
				printf("Setting bool value to false")
				val = "false"
			}
			b, err := strconv.ParseBool(val)
			if err != nil {
				return err
			}
			gfi.goValue.SetBool(b)
		case reflect.Int:
			i, err := strconv.ParseInt(numVal(val), 10, 64)
			if err != nil {
				return err
			}
			gfi.goValue.SetInt(i)
		case reflect.Int8:
			i, err := strconv.ParseInt(numVal(val), 10, 8)
			if err != nil {
				return err
			}
			gfi.goValue.SetInt(i)
		case reflect.Int16:
			i, err := strconv.ParseInt(numVal(val), 10, 16)
			if err != nil {
				return err
			}
			gfi.goValue.SetInt(i)
		case reflect.Int32:
			i, err := strconv.ParseInt(numVal(val), 10, 32)
			if err != nil {
				return err
			}
			gfi.goValue.SetInt(i)
		case reflect.Int64:
			i, err := strconv.ParseInt(numVal(val), 10, 64)
			if err != nil {
				return err
			}
			gfi.goValue.SetInt(i)
		case reflect.Uint:
			i, err := strconv.ParseUint(numVal(val), 10, 64)
			if err != nil {
				return err
			}
			gfi.goValue.SetUint(i)
		case reflect.Uint8:
			i, err := strconv.ParseUint(numVal(val), 10, 8)
			if err != nil {
				return err
			}
			gfi.goValue.SetUint(i)
		case reflect.Uint16:
			i, err := strconv.ParseUint(numVal(val), 10, 16)
			if err != nil {
				return err
			}
			gfi.goValue.SetUint(i)
		case reflect.Uint32:
			i, err := strconv.ParseUint(numVal(val), 10, 32)
			if err != nil {
				return err
			}
			gfi.goValue.SetUint(i)
		case reflect.Uint64:
			i, err := strconv.ParseUint(numVal(val), 10, 64)
			if err != nil {
				return err
			}
			gfi.goValue.SetUint(i)
		case reflect.Float32:
			f, err := strconv.ParseFloat(numVal(val), 32)
			if err != nil {
				return err
			}
			gfi.goValue.SetFloat(f)
		case reflect.Float64:
			f, err := strconv.ParseFloat(numVal(val), 64)
			if err != nil {
				return err
			}
			gfi.goValue.SetFloat(f)
		case reflect.String:
			gfi.goValue.SetString(val)
		default:
			return ErrUnsupportedFieldType
		}
	}

	return nil
}

func (gfi *Gofiguritem) populateSliceType(order []string) error {
	var prevVal *[]string

	for _, source := range order {
		kn := gfi.field
		if k, ok := gfi.keys[source]; ok {
			kn = k
		}

		printf("Looking for field '%s' with key '%s' in source '%s'", gfi.field, kn, source)
		val, err := Sources[source].GetArray(kn, prevVal)
		if err != nil {
			return err
		}

		// This causes duplication between array sources depending on order
		//prevVal = &val

		printf("Got value '%+v' from array source '%s' for key '%s'", val, source, gfi.field)

		switch gfi.goField.Type.Kind() {
		case reflect.Slice:
			switch gfi.goField.Type.Elem().Kind() {
			case reflect.String:
				for _, s := range val {
					printf("Appending string value '%s' to slice", s)
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(s)))
				}
			case reflect.Int:
				for _, s := range val {
					printf("Appending int value '%s' to slice", s)
					i, err := strconv.ParseInt(numVal(s), 10, 64)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(int(i))))
				}
			case reflect.Int8:
				for _, s := range val {
					printf("Appending int8 value '%s' to slice", s)
					i, err := strconv.ParseInt(numVal(s), 10, 8)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(int8(i))))
				}
			case reflect.Int16:
				for _, s := range val {
					printf("Appending int16 value '%s' to slice", s)
					i, err := strconv.ParseInt(numVal(s), 10, 16)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(int16(i))))
				}
			case reflect.Int32:
				for _, s := range val {
					printf("Appending int32 value '%s' to slice", s)
					i, err := strconv.ParseInt(numVal(s), 10, 32)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(int32(i))))
				}
			case reflect.Int64:
				for _, s := range val {
					printf("Appending int64 value '%s' to slice", s)
					i, err := strconv.ParseInt(numVal(s), 10, 64)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(int64(i))))
				}
			case reflect.Uint:
				for _, s := range val {
					printf("Appending uint value '%s' to slice", s)
					i, err := strconv.ParseUint(numVal(s), 10, 64)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(uint(i))))
				}
			case reflect.Uint8:
				for _, s := range val {
					printf("Appending uint8 value '%s' to slice", s)
					i, err := strconv.ParseUint(numVal(s), 10, 8)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(uint8(i))))
				}
			case reflect.Uint16:
				for _, s := range val {
					printf("Appending uint16 value '%s' to slice", s)
					i, err := strconv.ParseUint(numVal(s), 10, 16)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(uint16(i))))
				}
			case reflect.Uint32:
				for _, s := range val {
					printf("Appending uint32 value '%s' to slice", s)
					i, err := strconv.ParseUint(numVal(s), 10, 32)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(uint32(i))))
				}
			case reflect.Uint64:
				for _, s := range val {
					printf("Appending uint64 value '%s' to slice", s)
					i, err := strconv.ParseUint(numVal(s), 10, 64)
					if err != nil {
						return err
					}
					gfi.goValue.Set(reflect.Append(gfi.goValue, reflect.ValueOf(uint64(i))))
				}
			// TODO floats
			default:
				//return ErrUnsupportedFieldType
			}
		}
	}

	return nil
}

func (gfg *Gofiguration) populateStruct() error {
	for _, gfi := range gfg.fields {
		printf("Populating field %s", gfi.field)
		switch gfi.goField.Type.Kind() {
		case reflect.Invalid:
			return ErrUnsupportedFieldType
		case reflect.Uintptr:
			return ErrUnsupportedFieldType
		case reflect.Complex64:
			return ErrUnsupportedFieldType
		case reflect.Complex128:
			return ErrUnsupportedFieldType
		case reflect.Chan:
			return ErrUnsupportedFieldType
		case reflect.Func:
			return ErrUnsupportedFieldType
		case reflect.Ptr:
			return ErrUnsupportedFieldType
		case reflect.UnsafePointer:
			return ErrUnsupportedFieldType
		case reflect.Interface:
			// TODO
			return ErrUnsupportedFieldType
		case reflect.Map:
			// TODO
			return ErrUnsupportedFieldType
		case reflect.Slice:
			printf("Calling populateSliceType")
			err := gfi.populateSliceType(gfg.order)
			if err != nil {
				return err
			}
		case reflect.Struct:
			// TODO
			return ErrUnsupportedFieldType
		case reflect.Array:
			// TODO
			return ErrUnsupportedFieldType
		default:
			printf("Calling populateDefaultType")
			err := gfi.populateDefaultType(gfg.order)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// Apply applies the Gofiguration to the struct
func (gfg *Gofiguration) Apply(s interface{}) error {
	defer gfg.cleanupSources()

	err := gfg.initSources()
	if err != nil {
		return err
	}

	err = gfg.registerFields()
	if err != nil {
		return err
	}

	return gfg.populateStruct()
}

// Gofigure parses and applies the configuration defined by the struct
func Gofigure(s interface{}) error {
	gfg, err := ParseStruct(s)
	if err != nil {
		return err
	}
	return gfg.Apply(s)
}
