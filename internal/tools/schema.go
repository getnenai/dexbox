package tools

import (
	"reflect"
	"strings"
)

// ComputerParams defines the parameters for the computer tool.
type ComputerParams struct {
	Action          string  `json:"action" enum:"screenshot,left_click,right_click,middle_click,double_click,mouse_move,scroll,type,key,left_click_drag,cursor_position" required:"true" description:"The action to perform"`
	Coordinate      *[2]int `json:"coordinate,omitempty" description:"Target [x, y] position for click/move/scroll actions"`
	Text            string  `json:"text,omitempty" description:"Text to type or key to press"`
	StartCoordinate *[2]int `json:"start_coordinate,omitempty" description:"Start [x, y] for left_click_drag"`
	Direction       string  `json:"direction,omitempty" enum:"up,down" description:"Scroll direction (default: down)"`
	Amount          int     `json:"amount,omitempty" description:"Scroll amount in lines (default: 3)"`
}

// BashParams defines the parameters for the bash tool.
type BashParams struct {
	Command string `json:"command" required:"true" description:"PowerShell command to execute"`
}

// EditorParams defines the parameters for the text_editor tool.
type EditorParams struct {
	Command    string  `json:"command" enum:"view,create,str_replace,insert,undo_edit" required:"true" description:"Editor command to execute"`
	Path       string  `json:"path" required:"true" description:"File path on the VM"`
	FileText   string  `json:"file_text,omitempty" description:"Content for create command"`
	OldStr     string  `json:"old_str,omitempty" description:"Text to find for str_replace"`
	NewStr     string  `json:"new_str,omitempty" description:"Replacement text for str_replace or text to insert"`
	InsertLine int     `json:"insert_line,omitempty" description:"Line number for insert command"`
	ViewRange  *[2]int `json:"view_range,omitempty" description:"[start_line, end_line] range for view command"`
}

// ToolSchema represents a tool's full schema for the GET /tools endpoint.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`

	// Computer-specific display metadata (omitted for other tools).
	DisplayWidth  *int `json:"display_width_px,omitempty"`
	DisplayHeight *int `json:"display_height_px,omitempty"`
}

// ToolDef defines a tool with its metadata and param type for the registry.
type ToolDef struct {
	Name        string
	Description string
	ParamType   any // zero-value instance of the param struct
}

// ToolRegistry lists all tools exposed by the server.
var ToolRegistry = []ToolDef{
	{
		Name:        "computer",
		Description: "Computer-use tool for controlling a Windows VM. Supports screenshot, click, type, key, scroll, drag, and cursor position actions.",
		ParamType:   ComputerParams{},
	},
	{
		Name:        "bash",
		Description: "Run a PowerShell command on the Windows guest VM.",
		ParamType:   BashParams{},
	},
	{
		Name:        "text_editor",
		Description: "View and edit files on the Windows VM. Supports view, create, str_replace, insert, and undo_edit commands.",
		ParamType:   EditorParams{},
	},
}

// AllToolSchemas generates JSON Schemas for all registered tools.
func AllToolSchemas(display DisplayConfig) []ToolSchema {
	schemas := make([]ToolSchema, 0, len(ToolRegistry))
	for _, def := range ToolRegistry {
		s := ToolSchema{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  generateSchema(def.ParamType),
		}
		if def.Name == "computer" {
			w, h := display.Width, display.Height
			s.DisplayWidth = &w
			s.DisplayHeight = &h
		}
		schemas = append(schemas, s)
	}
	return schemas
}

// generateSchema produces a JSON Schema object from a param struct using reflection.
func generateSchema(params any) map[string]any {
	t := reflect.TypeOf(params)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	properties := map[string]any{}
	var required []string

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		name := strings.Split(jsonTag, ",")[0]

		prop := map[string]any{}

		// Determine JSON Schema type from Go type.
		prop["type"] = goTypeToJSONSchemaType(field.Type)

		// Array fields: [2]int or *[2]int become {type: array, items: {type: number/integer}, ...}
		if isArrayType(field.Type) {
			elemType := arrayElemType(field.Type)
			prop["type"] = "array"
			prop["items"] = map[string]any{"type": elemType}
			if n := arrayLen(field.Type); n > 0 {
				prop["minItems"] = n
				prop["maxItems"] = n
			}
		}

		if desc := field.Tag.Get("description"); desc != "" {
			prop["description"] = desc
		}

		if enum := field.Tag.Get("enum"); enum != "" {
			values := strings.Split(enum, ",")
			prop["enum"] = values
		}

		if field.Tag.Get("required") == "true" {
			required = append(required, name)
		}

		properties[name] = prop
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func goTypeToJSONSchemaType(t reflect.Type) string {
	// Unwrap pointer
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Array, reflect.Slice:
		return "array"
	default:
		return "string"
	}
}

func isArrayType(t reflect.Type) bool {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.Array || t.Kind() == reflect.Slice
}

func arrayElemType(t reflect.Type) string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return goTypeToJSONSchemaType(t.Elem())
}

func arrayLen(t reflect.Type) int {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() == reflect.Array {
		return t.Len()
	}
	return 0
}
