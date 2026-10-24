package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/store"
)

// Call outcomes: the one low-cardinality set used for the metric label, the
// audit outcome and the span attribute.
const (
	outcomeOK           = "ok"
	outcomeInvalidInput = "invalid_input"
	outcomeDenied       = "denied"
	outcomeTooLarge     = "too_large"
	outcomeError        = "error"
)

// maxOutputBytes caps a tool's JSON output.
const maxOutputBytes = 256 << 10

// toolError is an error the agent may see verbatim, with its outcome.
// Any other error is internal: logged, and shown to the agent as "internal error".
type toolError struct{ outcome, msg string }

func (e *toolError) Error() string { return e.msg }

func invalidf(format string, args ...any) error {
	return &toolError{outcome: outcomeInvalidInput, msg: fmt.Sprintf(format, args...)}
}

// addTool registers fn as tool t through go-sdk's raw Server.AddTool.
//
// The input and output schemas are inferred exactly as the generic
// mcp.AddTool infers them, so tools/list is unchanged. The rest of what the
// generic path does moves into instrument, done once per call: arguments are
// validated against the resolved input schema (so a schema-invalid call is
// audited, counted and traced), and the output is marshalled once for the
// size cap, structuredContent and the text content. The output is not
// validated against its schema: coachd builds the value, its type is the schema.
func addTool[In, Out any](s *svc, srv *mcp.Server, t *mcp.Tool, scope string, fn func(context.Context, In) (Out, error)) {
	if t.InputSchema != nil || t.OutputSchema != nil {
		panic(fmt.Sprintf("addTool: tool %q: schemas are inferred from In and Out", t.Name))
	}
	tt := *t
	var input *jsonschema.Resolved
	tt.InputSchema, input = inferSchema[In](t.Name)
	tt.OutputSchema, _ = inferSchema[Out](t.Name)
	srv.AddTool(&tt, instrument(s, t.Name, scope, input, fn))
}

// inferSchema infers and resolves T's schema the way go-sdk v1.8.0's
// setSchema does for mcp.AddTool (no schema cache). go-sdk special-cases
// pointer types (inferred from the element, nil output swapped for its zero
// value) and the empty interface; no tool needs either, so both are refused.
func inferSchema[T any](tool string) (*jsonschema.Schema, *jsonschema.Resolved) {
	rt := reflect.TypeFor[T]()
	if k := rt.Kind(); k == reflect.Pointer || k == reflect.Interface {
		panic(fmt.Sprintf("addTool: tool %q: unsupported type %v", tool, rt))
	}
	schema, err := jsonschema.ForType(rt, &jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("addTool: tool %q: %v", tool, err))
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		panic(fmt.Sprintf("addTool: tool %q: %v", tool, err))
	}
	return schema, resolved
}

// decodeArgs validates raw tool arguments against the input schema and
// decodes them into In, with go-sdk's semantics: absent arguments are {},
// defaults apply, and the value passes through map[string]any, so 5.0 is a
// valid integer. Failures are invalid_input with go-sdk's message.
func decodeArgs[In any](schema *jsonschema.Resolved, raw json.RawMessage) (In, error) {
	var in In
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return in, invalidf("validating \"arguments\": unmarshaling arguments: %v", err)
		}
	}
	var v any = m
	if err := schema.ApplyDefaults(&v); err != nil {
		return in, invalidf("validating \"arguments\": applying schema defaults:\n%v", err)
	}
	if err := schema.Validate(&v); err != nil {
		return in, invalidf("validating \"arguments\": %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return in, invalidf("validating \"arguments\": marshalling with defaults: %v", err)
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, &toolError{outcome: outcomeInvalidInput, msg: err.Error()}
	}
	return in, nil
}

// instrument wraps a tool: identity and scope check, argument validation,
// output size cap, one span, metrics and an audit event per call. Arguments
// and output never reach telemetry or the audit log. A tool error becomes
// an error result; success carries the one marshalled output as both
// structuredContent and a single text content, as go-sdk's generic path does.
func instrument[In, Out any](s *svc, name, scope string, input *jsonschema.Resolved,
	fn func(context.Context, In) (Out, error)) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		id, authed := auth.IdentityFrom(ctx)
		ctx, span := s.Tracer.Start(ctx, "mcp.tool", trace.WithAttributes(
			attribute.String("tool", name), attribute.String("lane", string(id.Lane)), attribute.String("client", id.Client)))
		defer span.End()

		out, err := func() ([]byte, error) {
			if !authed {
				return nil, &toolError{outcome: outcomeDenied, msg: "unauthenticated"}
			}
			if !id.Has(scope) {
				return nil, &toolError{outcome: outcomeDenied, msg: "insufficient scope"}
			}
			in, err := decodeArgs[In](input, req.Params.Arguments)
			if err != nil {
				return nil, err
			}
			v, err := fn(ctx, in)
			if err != nil {
				return nil, err
			}
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			if len(b) > maxOutputBytes {
				return nil, &toolError{outcome: outcomeTooLarge, msg: fmt.Sprintf(
					"response too large: %d KiB > %d KiB; narrow from/to", (len(b)+1023)/1024, maxOutputBytes>>10)}
			}
			return b, nil
		}()

		outcome := outcomeOK
		if err != nil {
			var te *toolError
			if errors.As(err, &te) {
				outcome = te.outcome
			} else {
				outcome = outcomeError
				s.Log.ErrorContext(ctx, "mcp tool failed", "tool", name, "outcome", outcome, "error", err)
				err = errors.New("internal error")
				span.SetStatus(codes.Error, "internal error")
			}
		}
		span.SetAttributes(attribute.String("outcome", outcome))
		if s.Metrics != nil {
			attrs := metric.WithAttributes(attribute.String("tool", name), attribute.String("client", id.Client),
				attribute.String("outcome", outcome))
			s.Metrics.MCPCalls.Add(ctx, 1, attrs)
			s.Metrics.MCPDuration.Record(ctx, time.Since(start).Seconds(), attrs)
		}
		if s.Audit != nil {
			ev := store.AuditEvent{At: s.Now().UTC(), Lane: string(id.Lane), Actor: id.Subject, Client: id.Client,
				Event: "tool_call", Target: name, Outcome: outcome}
			if aerr := s.Audit.AppendAudit(context.WithoutCancel(ctx), ev); aerr != nil {
				s.Log.ErrorContext(ctx, "mcp tool audit failed", "tool", name, "outcome", outcome, "error", aerr)
			}
		}
		if err != nil {
			var res mcp.CallToolResult
			res.SetError(err)
			return &res, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
			StructuredContent: json.RawMessage(out)}, nil
	}
}
