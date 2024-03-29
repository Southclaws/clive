package clive

import (
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

// Build constructs a urfave/cli App from an instance of a decorated struct
// Since it is designed to be used 1. on initialisation and; 2. with static data
// that is compile-time only - it does not return an error but instead panics.
// The idea is you will do all your setup once and as long as it doesn't change
// this will never break, so there is little need to pass errors back.
func Build(objs ...interface{}) (c *cli.App) {
	c, err := build(objs...)
	if err != nil {
		panic(err)
	}
	return
}

// Flags is a helper function for use within a command Action function. It takes
// an instance of the struct that was used to generate the command and the
// cli.Context pointer that is passed to the action function. It will then
// call the necessary flag access functions (such as c.String("...")) and return
// an instance of the input struct with the necessary fields set.
func Flags(obj interface{}, c *cli.Context) (result interface{}) {
	if obj == nil {
		panic("obj is null")
	}

	objValue := reflect.ValueOf(obj)
	for objValue.Kind() == reflect.Ptr {
		objValue = objValue.Elem()
	}

	objType := objValue.Type()

	resultValue := reflect.New(objType).Elem()

	for i := 0; i < objType.NumField(); i++ {
		fieldType := objType.Field(i)
		cmdmeta, err := parseMeta(fieldType.Tag.Get("cli"))
		if err != nil {
			panic(err)
		}

		if strings.HasPrefix(fieldType.Name, "Flag") {
			flag, err := flagFromType(fieldType, cmdmeta)
			if err != nil {
				panic(errors.Wrap(err, "failed to generate flag from struct field"))
			}

			switch fieldType.Type.String() {
			case "int":
				resultValue.FieldByName(fieldType.Name).SetInt(int64(c.Int(flag.GetName())))
			case "int64":
				resultValue.FieldByName(fieldType.Name).SetInt(c.Int64(flag.GetName()))
			case "uint":
				resultValue.FieldByName(fieldType.Name).SetUint(uint64(c.Uint(flag.GetName())))
			case "uint64":
				resultValue.FieldByName(fieldType.Name).SetUint(c.Uint64(flag.GetName()))
			case "float32":
				resultValue.FieldByName(fieldType.Name).SetFloat(c.Float64(flag.GetName()))
			case "float64":
				resultValue.FieldByName(fieldType.Name).SetFloat(c.Float64(flag.GetName()))
			case "bool":
				resultValue.FieldByName(fieldType.Name).SetBool(c.Bool(flag.GetName()))
			case "string":
				resultValue.FieldByName(fieldType.Name).SetString(c.String(flag.GetName()))
			case "time.Duration":
				resultValue.FieldByName(fieldType.Name).SetInt(c.Duration(flag.GetName()).Nanoseconds())
			case "[]int":
				resultValue.FieldByName(fieldType.Name).Set(genericSliceOf(c.IntSlice(flag.GetName())))
			case "[]int64":
				resultValue.FieldByName(fieldType.Name).Set(genericSliceOf(c.Int64Slice(flag.GetName())))
			// case "[]uint":
			// 	resultValue.FieldByName(fieldType.Name).Set(genericSliceOf(c.IntSlice(flag.GetName())))
			// case "[]uint64":
			// 	resultValue.FieldByName(fieldType.Name).Set(genericSliceOf(c.Int64Slice(flag.GetName())))
			case "[]string":
				resultValue.FieldByName(fieldType.Name).Set(genericSliceOf(c.StringSlice(flag.GetName())))
			default:
				panic("unsupported type")
			}
		}
	}

	return resultValue.Interface()
}

// given a generic slice type, returns a reflected version of that slice with
// all elements inserted.
func genericSliceOf(slice interface{}) reflect.Value {
	sliceValue := reflect.ValueOf(slice)
	length := sliceValue.Len()
	sliceAddr := reflect.New(reflect.MakeSlice(
		reflect.TypeOf(slice),
		length,
		length,
	).Type())
	for i := 0; i < length; i++ {
		value := sliceValue.Index(i)
		ap := reflect.Append(sliceAddr.Elem(), value)
		sliceAddr.Elem().Set(ap)
	}
	return sliceAddr.Elem()
}

func build(objs ...interface{}) (c *cli.App, err error) {
	c = cli.NewApp()

	commands := []cli.Command{}
	for _, obj := range objs {
		var command *cli.Command
		command, err = commandFromObject(obj)
		if err != nil {
			return
		}
		commands = append(commands, *command)
	}

	// if it's a one-command application, there's no need for a subcommand so
	// just move the command's contents into the root object, aka the 'App'
	if len(commands) == 1 {
		c.Usage = commands[0].Usage
		c.Action = commands[0].Action
		c.Flags = commands[0].Flags
	} else {
		c.Commands = commands
		c.Flags = nil
	}
	return
}

type commandMetadata struct {
	Name    string
	Usage   string
	Hidden  bool
	Default string
}

func commandFromObject(obj interface{}) (command *cli.Command, err error) {
	if obj == nil {
		return nil, errors.New("obj is null")
	}

	// recursively dereference
	objValue := reflect.ValueOf(obj)
	for objValue.Kind() == reflect.Ptr {
		objValue = objValue.Elem()
	}

	// anonymous structs (struct{ ... }{}) are not allowed
	objType := objValue.Type()
	if objType.Name() == "" {
		return nil, errors.New("need a named struct type to determine command name")
	}

	// the first field must be an embedded cli.Command struct
	command, err = getCommand(objType.Field(0), objValue.Field(0))
	if err != nil {
		return nil, err
	}
	command.Name = strings.ToLower(objType.Name())

	for i := 1; i < objType.NumField(); i++ {
		fieldType := objType.Field(i)

		cmdmeta, err := parseMeta(fieldType.Tag.Get("cli"))
		if err != nil {
			return nil, err
		}

		// automatically turn fields that begin with Flag into cli.Flag objects
		if strings.HasPrefix(fieldType.Name, "Flag") {
			flag, err := flagFromType(fieldType, cmdmeta)
			if err != nil {
				return nil, errors.Wrap(err, "failed to generate flag from struct field")
			}
			command.Flags = append(command.Flags, flag)
		}
	}

	return command, nil
}

func getCommand(fieldType reflect.StructField, fieldValue reflect.Value) (c *cli.Command, err error) {
	if fieldType.Name != "Command" {
		return nil, errors.New("first field must be an embedded cli.Command")
	}

	if fieldValue.Kind() != reflect.Struct {
		return nil, errors.New("expected Command field to be a struct (specifically, an embedded cli.Command struct)")
	}

	cmd, ok := fieldValue.Interface().(cli.Command)
	if !ok {
		return nil, errors.New("failed to cast Command field to a cli.Command object")
	}

	cmdmeta, err := parseMeta(fieldType.Tag.Get("cli"))
	if err != nil {
		return nil, errors.Wrap(err, "failed to read cmdmeta tag on the embedded cli.Command struct")
	}
	cmd.Usage = cmdmeta.Usage
	cmd.Flags = []cli.Flag{}

	return &cmd, nil
}

func parseMeta(s string) (cmdmeta commandMetadata, err error) {
	// this code allows strings to be placed inside single-quotes in order to
	// escape comma characters.
	quotes := false
	sections := strings.FieldsFunc(s, func(r rune) bool {
		if r == '\'' && !quotes {
			quotes = true
		} else if r == '\'' && quotes {
			quotes = false
		}
		if r == ',' && !quotes {
			return true
		}
		return false
	})
	for _, section := range sections {
		keyvalue := strings.SplitN(section, ":", 2)
		if len(keyvalue) == 2 {
			switch keyvalue[0] {
			case "name":
				cmdmeta.Name = keyvalue[1]
			case "usage":
				cmdmeta.Usage = strings.Trim(keyvalue[1], "'") // trim single-quotes
			case "hidden":
				cmdmeta.Hidden, err = strconv.ParseBool(keyvalue[1])
				if err != nil {
					err = errors.Wrap(err, "failed to parse 'hidden' as a bool")
				}
			case "default":
				cmdmeta.Default = keyvalue[1]
			default:
				err = errors.Errorf("unknown command tag: '%s:%s'", keyvalue[0], keyvalue[1])
			}
		} else {
			err = errors.Errorf("malformed tag: '%s'", section)
		}
		if err != nil {
			return
		}
	}
	return cmdmeta, err
}

//nolint:errcheck
func flagFromType(fieldType reflect.StructField, cmdmeta commandMetadata) (flag cli.Flag, err error) {
	var (
		name string
		env  string
	)

	if cmdmeta.Name != "" {
		name = strcase.ToKebab(cmdmeta.Name)
	} else {
		name = strcase.ToKebab(strings.TrimPrefix(fieldType.Name, "Flag"))
	}
	env = strcase.ToScreamingSnake(name)

	cmdmeta.Default = strings.Trim(cmdmeta.Default, "'")

	switch fieldType.Type.String() {
	case "int":
		def, _ := strconv.ParseInt(cmdmeta.Default, 10, 64)
		flag = cli.IntFlag{
			Name:   name,
			EnvVar: env,
			Value:  int(def),
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "int64":
		def, _ := strconv.ParseInt(cmdmeta.Default, 10, 64)
		flag = cli.Int64Flag{
			Name:   name,
			EnvVar: env,
			Value:  def,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "uint":
		def, _ := strconv.ParseUint(cmdmeta.Default, 10, 64)
		flag = cli.UintFlag{
			Name:   name,
			EnvVar: env,
			Value:  uint(def),
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "uint64":
		def, _ := strconv.ParseUint(cmdmeta.Default, 10, 64)
		flag = cli.Uint64Flag{
			Name:   name,
			EnvVar: env,
			Value:  def,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "float32":
		def, _ := strconv.ParseFloat(cmdmeta.Default, 32)
		flag = cli.Float64Flag{
			Name:   name,
			EnvVar: env,
			Value:  def,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "float64":
		def, _ := strconv.ParseFloat(cmdmeta.Default, 64)
		flag = cli.Float64Flag{
			Name:   name,
			EnvVar: env,
			Value:  def,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "bool":
		def, _ := strconv.ParseBool(cmdmeta.Default)
		if !def {
			flag = cli.BoolFlag{
				Name:   name,
				EnvVar: env,
				Hidden: cmdmeta.Hidden,
				Usage:  cmdmeta.Usage,
			}
		} else {
			flag = cli.BoolTFlag{
				Name:   name,
				EnvVar: env,
				Hidden: cmdmeta.Hidden,
				Usage:  cmdmeta.Usage,
			}
		}

	case "string":
		flag = cli.StringFlag{
			Name:   name,
			EnvVar: env,
			Value:  cmdmeta.Default,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "time.Duration":
		def, _ := time.ParseDuration(cmdmeta.Default)
		flag = cli.DurationFlag{
			Name:   name,
			EnvVar: env,
			Value:  def,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "[]int":
		var def *cli.IntSlice // must remain nil if unset
		if cmdmeta.Default != "" {
			def = &cli.IntSlice{}
			for _, s := range strings.Split(cmdmeta.Default, ",") {
				d, _ := strconv.Atoi(s)
				*def = append(*def, d)
			}
		}
		flag = cli.IntSliceFlag{
			Name:   name,
			EnvVar: env,
			Value:  def,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	case "[]int64":
		var def *cli.Int64Slice // must remain nil if unset
		if cmdmeta.Default != "" {
			def = &cli.Int64Slice{}
			for _, s := range strings.Split(cmdmeta.Default, ",") {
				d, _ := strconv.Atoi(s)
				*def = append(*def, int64(d))
			}
		}
		flag = cli.Int64SliceFlag{
			Name:   name,
			EnvVar: env,
			Value:  def,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	// urfave/cli does not have unsigned types yet
	// case "[]uint":
	// 	flag = cli.IntSliceFlag{
	// 		Name:   name,
	// 		EnvVar: env,
	// 		Hidden: cmdmeta.Hidden,
	// 		Usage:  cmdmeta.Usage,
	// 	}

	// case "[]uint64":
	// 	flag = cli.Int64SliceFlag{
	// 		Name:   name,
	// 		EnvVar: env,
	// 		Hidden: cmdmeta.Hidden,
	// 		Usage:  cmdmeta.Usage,
	// 	}

	case "[]string":
		var def *cli.StringSlice // must remain nil if unset
		if cmdmeta.Default != "" {
			def = &cli.StringSlice{}
			*def = strings.Split(cmdmeta.Default, ",")
		}
		flag = cli.StringSliceFlag{
			Name:   name,
			EnvVar: env,
			Value:  def,
			Hidden: cmdmeta.Hidden,
			Usage:  cmdmeta.Usage,
		}

	default:
		err = errors.Errorf("unsupported flag generator type: %s", fieldType.Type.String())
	}

	return flag, err
}
