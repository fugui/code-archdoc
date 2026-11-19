package codeir

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"code-archdoc/pkg/security"

	"github.com/emicklei/proto"
)

// ProtoExtractor 纯 Go Protobuf / CROS RPC AST 提取器（ADR-02：100% 精确率）。
type ProtoExtractor struct {
	redactor *security.SecretRedactor
}

func NewProtoExtractor() *ProtoExtractor {
	return &ProtoExtractor{
		redactor: security.NewSecretRedactor(),
	}
}

func (e *ProtoExtractor) Name() string { return "proto-ast" }

func (e *ProtoExtractor) SupportedLanguages() []string { return []string{"proto"} }

func (e *ProtoExtractor) ExtractFile(ctx context.Context, file FileEntry, content []byte) (*ExtractionResult, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	parser := proto.NewParser(bytes.NewReader(content))
	definition, err := parser.Parse()
	if err != nil {
		return &ExtractionResult{
			Diagnostics: []ParseDiagnostic{{
				FilePath: file.RelPath,
				Message:  fmt.Sprintf("解析 proto 失败: %v", err),
			}},
		}, nil
	}

	totalLines := countLines(content)
	pf := ProtoFileFact{
		FilePath:    file.RelPath,
		Syntax:      "proto2",
		CrosOptions: make(map[string]string),
		Meta: FactMeta{
			Confidence: ConfidenceExact,
			Span: SourceSpan{
				FilePath:  file.RelPath,
				LineStart: 1,
				LineEnd:   totalLines,
			},
			ContentFingerprint: fingerprintString(string(content)),
		},
	}

	// 先收集 syntax / package / imports / file options
	for _, elem := range definition.Elements {
		switch v := elem.(type) {
		case *proto.Syntax:
			pf.Syntax = v.Value
		case *proto.Package:
			pf.Package = v.Name
		case *proto.Import:
			pf.Imports = append(pf.Imports, v.Filename)
		case *proto.Option:
			k := normalizeProtoOptionName(v.Name)
			pf.CrosOptions[k] = formatProtoLiteral(v.Constant)
		}
	}

	// 再收集 service / message / enum（需要 package 已解析）
	for _, elem := range definition.Elements {
		switch v := elem.(type) {
		case *proto.Service:
			pf.Services = append(pf.Services, e.extractService(file.RelPath, pf.Package, v))
		case *proto.Message:
			msgs, enums := e.extractMessageTree(file.RelPath, pf.Package, "", v)
			pf.Messages = append(pf.Messages, msgs...)
			pf.Enums = append(pf.Enums, enums...)
		case *proto.Enum:
			pf.Enums = append(pf.Enums, e.extractEnum(file.RelPath, pf.Package, "", v))
		}
	}

	if len(pf.CrosOptions) == 0 {
		pf.CrosOptions = nil
	}
	sort.Strings(pf.Imports)

	return &ExtractionResult{
		IDLContracts: []ProtoFileFact{pf},
	}, nil
}

func (e *ProtoExtractor) extractService(filePath, pkg string, svc *proto.Service) ProtoServiceFact {
	fullName := qualifyProtoName(pkg, svc.Name)
	startLine := svc.Position.Line
	endLine := startLine

	svcOpts := make(map[string]string)
	var rpcs []ProtoRPCFact

	for _, child := range svc.Elements {
		switch c := child.(type) {
		case *proto.Option:
			svcOpts[normalizeProtoOptionName(c.Name)] = formatProtoLiteral(c.Constant)
			if c.Position.Line > endLine {
				endLine = c.Position.Line
			}
		case *proto.RPC:
			rpcFact := e.extractRPC(filePath, fullName, c)
			rpcs = append(rpcs, rpcFact)
			if rpcFact.Meta.Span.LineEnd > endLine {
				endLine = rpcFact.Meta.Span.LineEnd
			}
		}
	}

	if len(svcOpts) == 0 {
		svcOpts = nil
	}
	comment := e.cleanComment(svc.Comment)
	fpInput := fmt.Sprintf("service:%s:%d:%v", fullName, len(rpcs), svcOpts)
	return ProtoServiceFact{
		Name:        svc.Name,
		FullName:    fullName,
		CrosOptions: svcOpts,
		RPCs:        rpcs,
		Comment:     comment,
		Meta: FactMeta{
			Confidence: ConfidenceExact,
			Span: SourceSpan{
				FilePath:  filePath,
				LineStart: startLine,
				LineEnd:   endLine,
			},
			ContentFingerprint: fingerprintString(fpInput),
		},
	}
}

func (e *ProtoExtractor) extractRPC(filePath, svcFullName string, rpc *proto.RPC) ProtoRPCFact {
	startLine := rpc.Position.Line
	endLine := startLine
	opts := make(map[string]string)

	for _, opt := range rpc.Options {
		opts[normalizeProtoOptionName(opt.Name)] = formatProtoLiteral(opt.Constant)
		if opt.Position.Line > endLine {
			endLine = opt.Position.Line
		}
	}
	for _, elem := range rpc.Elements {
		if opt, ok := elem.(*proto.Option); ok {
			opts[normalizeProtoOptionName(opt.Name)] = formatProtoLiteral(opt.Constant)
			if opt.Position.Line > endLine {
				endLine = opt.Position.Line
			}
		}
	}

	if len(opts) == 0 {
		opts = nil
	}
	fpInput := fmt.Sprintf("rpc:%s/%s(%v:%s)->(%v:%s)%v",
		svcFullName, rpc.Name, rpc.StreamsRequest, rpc.RequestType, rpc.StreamsReturns, rpc.ReturnsType, opts)

	return ProtoRPCFact{
		MethodName:      rpc.Name,
		RequestType:     rpc.RequestType,
		ResponseType:    rpc.ReturnsType,
		ClientStreaming: rpc.StreamsRequest,
		ServerStreaming: rpc.StreamsReturns,
		CrosOptions:     opts,
		Comment:         e.cleanComment(rpc.Comment),
		Meta: FactMeta{
			Confidence: ConfidenceExact,
			Span: SourceSpan{
				FilePath:  filePath,
				LineStart: startLine,
				LineEnd:   endLine,
			},
			ContentFingerprint: fingerprintString(fpInput),
		},
	}
}

func (e *ProtoExtractor) extractMessageTree(filePath, pkg, parentPrefix string, msg *proto.Message) ([]ProtoMessageFact, []ProtoEnumFact) {
	localName := msg.Name
	if parentPrefix != "" {
		localName = parentPrefix + "." + msg.Name
	}
	fullName := qualifyProtoName(pkg, localName)
	startLine := msg.Position.Line
	endLine := startLine

	var fields []ProtoFieldFact
	var nestedMsgs []ProtoMessageFact
	var nestedEnums []ProtoEnumFact

	for _, child := range msg.Elements {
		switch c := child.(type) {
		case *proto.NormalField:
			if c.Position.Line > endLine {
				endLine = c.Position.Line
			}
			fields = append(fields, ProtoFieldFact{
				Name:     c.Name,
				Type:     c.Type,
				Tag:      c.Sequence,
				Repeated: c.Repeated,
				Optional: c.Optional,
				Comment:  e.cleanComment(c.Comment),
			})
		case *proto.MapField:
			if c.Position.Line > endLine {
				endLine = c.Position.Line
			}
			fields = append(fields, ProtoFieldFact{
				Name:    c.Name,
				Type:    fmt.Sprintf("map<%s,%s>", c.KeyType, c.Type),
				Tag:     c.Sequence,
				Comment: e.cleanComment(c.Comment),
			})
		case *proto.Oneof:
			for _, oe := range c.Elements {
				if of, ok := oe.(*proto.OneOfField); ok {
					if of.Position.Line > endLine {
						endLine = of.Position.Line
					}
					fields = append(fields, ProtoFieldFact{
						Name:     of.Name,
						Type:     of.Type,
						Tag:      of.Sequence,
						Optional: true,
						Comment:  e.cleanComment(of.Comment),
					})
				}
			}
		case *proto.Message:
			cm, ce := e.extractMessageTree(filePath, pkg, localName, c)
			nestedMsgs = append(nestedMsgs, cm...)
			nestedEnums = append(nestedEnums, ce...)
		case *proto.Enum:
			nestedEnums = append(nestedEnums, e.extractEnum(filePath, pkg, localName, c))
		}
	}

	var sigBuilder strings.Builder
	sigBuilder.WriteString("message:" + fullName)
	for _, f := range fields {
		sigBuilder.WriteString(fmt.Sprintf("|%d:%s:%s", f.Tag, f.Name, f.Type))
	}

	cur := ProtoMessageFact{
		Name:     localName,
		FullName: fullName,
		Fields:   fields,
		Comment:  e.cleanComment(msg.Comment),
		Meta: FactMeta{
			Confidence: ConfidenceExact,
			Span: SourceSpan{
				FilePath:  filePath,
				LineStart: startLine,
				LineEnd:   endLine,
			},
			ContentFingerprint: fingerprintString(sigBuilder.String()),
		},
	}

	outMsgs := append([]ProtoMessageFact{cur}, nestedMsgs...)
	return outMsgs, nestedEnums
}

func (e *ProtoExtractor) extractEnum(filePath, pkg, parentPrefix string, en *proto.Enum) ProtoEnumFact {
	localName := en.Name
	if parentPrefix != "" {
		localName = parentPrefix + "." + en.Name
	}
	fullName := qualifyProtoName(pkg, localName)
	startLine := en.Position.Line
	endLine := startLine

	var vals []ProtoEnumValue
	for _, child := range en.Elements {
		if ef, ok := child.(*proto.EnumField); ok {
			if ef.Position.Line > endLine {
				endLine = ef.Position.Line
			}
			vals = append(vals, ProtoEnumValue{
				Name:   ef.Name,
				Number: ef.Integer,
			})
		}
	}

	var sigBuilder strings.Builder
	sigBuilder.WriteString("enum:" + fullName)
	for _, v := range vals {
		sigBuilder.WriteString(fmt.Sprintf("|%s=%d", v.Name, v.Number))
	}

	return ProtoEnumFact{
		Name:     localName,
		FullName: fullName,
		Values:   vals,
		Comment:  e.cleanComment(en.Comment),
		Meta: FactMeta{
			Confidence: ConfidenceExact,
			Span: SourceSpan{
				FilePath:  filePath,
				LineStart: startLine,
				LineEnd:   endLine,
			},
			ContentFingerprint: fingerprintString(sigBuilder.String()),
		},
	}
}

func (e *ProtoExtractor) cleanComment(c *proto.Comment) string {
	if c == nil || len(c.Lines) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.Lines))
	for _, line := range c.Lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	joined := strings.Join(parts, " ")
	safe, _ := e.redactor.RedactText(joined)
	return truncateString(safe, 200)
}

func normalizeProtoOptionName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "(") && strings.HasSuffix(trimmed, ")") {
		trimmed = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}
	return trimmed
}

func formatProtoLiteral(lit proto.Literal) string {
	if lit.Source != "" {
		return lit.Source
	}
	if len(lit.OrderedMap) > 0 {
		parts := make([]string, 0, len(lit.OrderedMap))
		for _, item := range lit.OrderedMap {
			if item.Literal != nil {
				parts = append(parts, item.Name+"="+formatProtoLiteral(*item.Literal))
			}
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return ""
}

func qualifyProtoName(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

func fingerprintString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func truncateString(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}
