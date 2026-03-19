package tools

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// EinoTool 将 ToolDef 适配为 Eino 的 InvokableTool 接口
type EinoTool struct {
	def *ToolDef
}

// NewEinoTool 包装一个 ToolDef 为 Eino InvokableTool
func NewEinoTool(def *ToolDef) tool.InvokableTool {
	return &EinoTool{def: def}
}

// Info 返回工具的元信息（名称、描述、参数 schema）
func (t *EinoTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	params := make(map[string]*schema.ParameterInfo)
	for _, p := range t.def.Parameters {
		params[p.Name] = &schema.ParameterInfo{
			Type:     toSchemaType(p.Type),
			Desc:     p.Description,
			Required: p.Required,
		}
	}

	info := &schema.ToolInfo{
		Name: t.def.Name,
		Desc: t.def.Description,
	}
	if len(params) > 0 {
		info.ParamsOneOf = schema.NewParamsOneOfByParams(params)
	}
	return info, nil
}

// InvokableRun 执行工具，接收 JSON 参数字符串，返回结果字符串
func (t *EinoTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args map[string]interface{}
	if argumentsInJSON != "" && argumentsInJSON != "{}" {
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return "", err
		}
	}
	if args == nil {
		args = make(map[string]interface{})
	}
	return t.def.Fn(ctx, args)
}

// toSchemaType 将字符串类型映射为 Eino schema.DataType
func toSchemaType(t string) schema.DataType {
	switch t {
	case "number":
		return schema.Number
	case "integer":
		return schema.Integer
	case "boolean":
		return schema.Boolean
	case "array":
		return schema.Array
	default:
		return schema.String
	}
}

// ToEinoTools 将 Registry 中所有工具转换为 Eino InvokableTool 列表
func (r *Registry) ToEinoTools() []tool.InvokableTool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]tool.InvokableTool, 0, len(r.tools))
	for _, def := range r.tools {
		result = append(result, NewEinoTool(def))
	}
	return result
}
