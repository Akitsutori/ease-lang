package ease

import (
	"encoding/binary"
	"math"
)

// WASM 1.0 value and block types.
const (
	wasmI32       byte = 0x7F
	wasmI64       byte = 0x7E
	wasmF32       byte = 0x7D
	wasmF64       byte = 0x7C
	wasmFuncType  byte = 0x60
	wasmBlockVoid byte = 0x40
)

// WASM 1.0 export/import descriptor kinds.
const (
	wasmKindFunc   byte = 0x00
	wasmKindTable  byte = 0x01
	wasmKindMem    byte = 0x02
	wasmKindGlobal byte = 0x03
)

// WASM 1.0 opcodes.
const (
	opUnreachable byte = 0x00
	opNop         byte = 0x01
	opBlock       byte = 0x02
	opLoop        byte = 0x03
	opIf          byte = 0x04
	opElse        byte = 0x05
	opEnd         byte = 0x0B
	opBr          byte = 0x0C
	opBrIf        byte = 0x0D
	opReturn      byte = 0x0F
	opCall        byte = 0x10
	opDrop        byte = 0x1A
	opSelect      byte = 0x1B

	opLocalGet  byte = 0x20
	opLocalSet  byte = 0x21
	opLocalTee  byte = 0x22
	opGlobalGet byte = 0x23
	opGlobalSet byte = 0x24

	opI32Load   byte = 0x28
	opI64Load   byte = 0x29
	opF64Load   byte = 0x2B
	opI32Load8U byte = 0x2D
	opI32Store  byte = 0x36
	opI64Store  byte = 0x37
	opF64Store  byte = 0x39
	opI32Store8 byte = 0x3A

	opMemorySize byte = 0x3F
	opMemoryGrow byte = 0x40

	opI32Const byte = 0x41
	opI64Const byte = 0x42
	opF64Const byte = 0x44

	opI32Eqz byte = 0x45
	opI32Eq  byte = 0x46
	opI32Ne  byte = 0x47
	opI32LtS byte = 0x48
	opI32LtU byte = 0x49
	opI32GtS byte = 0x4A
	opI32GtU byte = 0x4B
	opI32LeS byte = 0x4C
	opI32LeU byte = 0x4D
	opI32GeS byte = 0x4E
	opI32GeU byte = 0x4F

	opI64Eqz byte = 0x50
	opI64Eq  byte = 0x51
	opI64Ne  byte = 0x52

	opF64Eq byte = 0x61
	opF64Ne byte = 0x62
	opF64Lt byte = 0x63
	opF64Gt byte = 0x64
	opF64Le byte = 0x65
	opF64Ge byte = 0x66

	opI32Add  byte = 0x6A
	opI32Sub  byte = 0x6B
	opI32Mul  byte = 0x6C
	opI32DivS byte = 0x6D
	opI32DivU byte = 0x6E
	opI32RemS byte = 0x6F
	opI32RemU byte = 0x70
	opI32And  byte = 0x71
	opI32Or   byte = 0x72
	opI32Xor  byte = 0x73
	opI32Shl  byte = 0x74
	opI32ShrU byte = 0x76

	opI64Add  byte = 0x7C
	opI64Sub  byte = 0x7D
	opI64And  byte = 0x83
	opI64Or   byte = 0x84
	opI64Shl  byte = 0x86
	opI64ShrU byte = 0x88

	opF64Abs     byte = 0x99
	opF64Neg     byte = 0x9A
	opF64Ceil    byte = 0x9B
	opF64Floor   byte = 0x9C
	opF64Trunc   byte = 0x9D
	opF64Nearest byte = 0x9E
	opF64Sqrt    byte = 0x9F
	opF64Add     byte = 0xA0
	opF64Sub     byte = 0xA1
	opF64Mul     byte = 0xA2
	opF64Div     byte = 0xA3
	opF64Min     byte = 0xA4
	opF64Max     byte = 0xA5

	opI32WrapI64        byte = 0xA7
	opI32TruncF64S      byte = 0xAA
	opI64ExtendI32U     byte = 0xAD
	opI64TruncF64S      byte = 0xB0
	opF64ConvertI32S    byte = 0xB7
	opF64ConvertI32U    byte = 0xB8
	opF64ConvertI64S    byte = 0xB9
	opF64ReinterpretI64 byte = 0xBF
	opI64ReinterpretF64 byte = 0xBD
)

func appendULEB128(buf []byte, v uint32) []byte {
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		buf = append(buf, b)
		if v == 0 {
			break
		}
	}
	return buf
}

func appendSLEB128(buf []byte, v int64) []byte {
	for {
		b := byte(v & 0x7F)
		v >>= 7
		done := (v == 0 && (b&0x40) == 0) || (v == -1 && (b&0x40) != 0)
		if !done {
			b |= 0x80
		}
		buf = append(buf, b)
		if done {
			break
		}
	}
	return buf
}

func appendWasmString(buf []byte, s string) []byte {
	buf = appendULEB128(buf, uint32(len(s)))
	return append(buf, s...)
}

type wasmFuncSig struct {
	params  []byte
	results []byte
}

type wasmImport struct {
	module  string
	name    string
	typeIdx uint32
}

type wasmGlobal struct {
	valType byte
	mutable bool
	initOp  byte
	initVal int64
}

type wasmExport struct {
	name string
	kind byte
	idx  uint32
}

type wasmDataSeg struct {
	offset uint32
	data   []byte
}

type wasmFuncBody struct {
	typeIdx uint32
	locals  []byte
	code    []byte
}

type wasmCodeBuf struct {
	b []byte
}

func (c *wasmCodeBuf) op(opcodes ...byte) {
	c.b = append(c.b, opcodes...)
}

func (c *wasmCodeBuf) i32Const(v int32) {
	c.b = append(c.b, opI32Const)
	c.b = appendSLEB128(c.b, int64(v))
}

func (c *wasmCodeBuf) i64Const(v int64) {
	c.b = append(c.b, opI64Const)
	c.b = appendSLEB128(c.b, v)
}

func (c *wasmCodeBuf) f64Const(v float64) {
	c.b = append(c.b, opF64Const)
	var raw [8]byte
	binary.LittleEndian.PutUint64(raw[:], math.Float64bits(v))
	c.b = append(c.b, raw[:]...)
}

func (c *wasmCodeBuf) call(fnIdx uint32) {
	c.b = append(c.b, opCall)
	c.b = appendULEB128(c.b, fnIdx)
}

func (c *wasmCodeBuf) localGet(idx uint32) {
	c.b = append(c.b, opLocalGet)
	c.b = appendULEB128(c.b, idx)
}

func (c *wasmCodeBuf) localSet(idx uint32) {
	c.b = append(c.b, opLocalSet)
	c.b = appendULEB128(c.b, idx)
}

func (c *wasmCodeBuf) localTee(idx uint32) {
	c.b = append(c.b, opLocalTee)
	c.b = appendULEB128(c.b, idx)
}

func (c *wasmCodeBuf) globalGet(idx uint32) {
	c.b = append(c.b, opGlobalGet)
	c.b = appendULEB128(c.b, idx)
}

func (c *wasmCodeBuf) globalSet(idx uint32) {
	c.b = append(c.b, opGlobalSet)
	c.b = appendULEB128(c.b, idx)
}

func (c *wasmCodeBuf) memOp(op byte, align, offset uint32) {
	c.b = append(c.b, op)
	c.b = appendULEB128(c.b, align)
	c.b = appendULEB128(c.b, offset)
}

func (c *wasmCodeBuf) br(depth uint32) {
	c.b = append(c.b, opBr)
	c.b = appendULEB128(c.b, depth)
}

func (c *wasmCodeBuf) brIf(depth uint32) {
	c.b = append(c.b, opBrIf)
	c.b = appendULEB128(c.b, depth)
}

func (c *wasmCodeBuf) block(blockType byte) {
	c.b = append(c.b, opBlock, blockType)
}

func (c *wasmCodeBuf) loop(blockType byte) {
	c.b = append(c.b, opLoop, blockType)
}

func (c *wasmCodeBuf) ifOp(blockType byte) {
	c.b = append(c.b, opIf, blockType)
}

type wasmModule struct {
	types      []wasmFuncSig
	imports    []wasmImport
	funcs      []wasmFuncBody
	minPages   uint32
	globals    []wasmGlobal
	exports    []wasmExport
	data       []wasmDataSeg
	staticData []byte
	staticBase uint32
	strPool    map[string]uint64
}

func newWasmModule(staticBase uint32, minPages uint32) *wasmModule {
	return &wasmModule{
		minPages:   minPages,
		staticBase: staticBase,
		strPool:    make(map[string]uint64),
	}
}

func (m *wasmModule) internString(s string) uint64 {
	if packed, ok := m.strPool[s]; ok {
		return packed
	}
	ptr := m.staticBase + uint32(len(m.staticData))
	m.staticData = append(m.staticData, s...)
	for len(m.staticData)%4 != 0 {
		m.staticData = append(m.staticData, 0)
	}
	packed := (uint64(ptr) << 32) | uint64(uint32(len(s)))
	m.strPool[s] = packed
	return packed
}

func (m *wasmModule) heapBase() uint32 {
	end := m.staticBase + uint32(len(m.staticData))
	if rem := end % 8; rem != 0 {
		end += 8 - rem
	}
	return end
}

func (m *wasmModule) addType(params, results []byte) uint32 {
	for i, t := range m.types {
		if bytesEqual(t.params, params) && bytesEqual(t.results, results) {
			return uint32(i)
		}
	}
	idx := uint32(len(m.types))
	m.types = append(m.types, wasmFuncSig{
		params:  append([]byte(nil), params...),
		results: append([]byte(nil), results...),
	})
	return idx
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *wasmModule) addImportFunc(mod, name string, params, results []byte) uint32 {
	tIdx := m.addType(params, results)
	fnIdx := uint32(len(m.imports))
	m.imports = append(m.imports, wasmImport{module: mod, name: name, typeIdx: tIdx})
	return fnIdx
}

func (m *wasmModule) reserveFunc(params, results []byte) uint32 {
	tIdx := m.addType(params, results)
	fnIdx := uint32(len(m.imports) + len(m.funcs))
	m.funcs = append(m.funcs, wasmFuncBody{typeIdx: tIdx})
	return fnIdx
}

func (m *wasmModule) setFuncBody(fnIdx uint32, locals []byte, code []byte) {
	localIdx := int(fnIdx) - len(m.imports)
	m.funcs[localIdx].locals = locals
	m.funcs[localIdx].code = code
}

func (m *wasmModule) addGlobal(valType byte, mutable bool, initOp byte, initVal int64) uint32 {
	idx := uint32(len(m.globals))
	m.globals = append(m.globals, wasmGlobal{valType: valType, mutable: mutable, initOp: initOp, initVal: initVal})
	return idx
}

func (m *wasmModule) addExport(name string, kind byte, idx uint32) {
	m.exports = append(m.exports, wasmExport{name: name, kind: kind, idx: idx})
}

func appendSection(out []byte, id byte, payload []byte) []byte {
	out = append(out, id)
	out = appendULEB128(out, uint32(len(payload)))
	return append(out, payload...)
}

func (m *wasmModule) emit() []byte {
	out := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00} // \0asm v1

	// 1. Type section
	if len(m.types) > 0 {
		var p []byte
		p = appendULEB128(p, uint32(len(m.types)))
		for _, t := range m.types {
			p = append(p, wasmFuncType)
			p = appendULEB128(p, uint32(len(t.params)))
			p = append(p, t.params...)
			p = appendULEB128(p, uint32(len(t.results)))
			p = append(p, t.results...)
		}
		out = appendSection(out, 1, p)
	}

	// 2. Import section
	if len(m.imports) > 0 {
		var p []byte
		p = appendULEB128(p, uint32(len(m.imports)))
		for _, imp := range m.imports {
			p = appendWasmString(p, imp.module)
			p = appendWasmString(p, imp.name)
			p = append(p, wasmKindFunc)
			p = appendULEB128(p, imp.typeIdx)
		}
		out = appendSection(out, 2, p)
	}

	// 3. Function section
	if len(m.funcs) > 0 {
		var p []byte
		p = appendULEB128(p, uint32(len(m.funcs)))
		for _, fn := range m.funcs {
			p = appendULEB128(p, fn.typeIdx)
		}
		out = appendSection(out, 3, p)
	}

	// 5. Memory section
	{
		var p []byte
		p = appendULEB128(p, 1)
		p = append(p, 0x00) // min only
		p = appendULEB128(p, m.minPages)
		out = appendSection(out, 5, p)
	}

	// 6. Global section
	if len(m.globals) > 0 {
		var p []byte
		p = appendULEB128(p, uint32(len(m.globals)))
		for _, g := range m.globals {
			p = append(p, g.valType)
			if g.mutable {
				p = append(p, 1)
			} else {
				p = append(p, 0)
			}
			p = append(p, g.initOp)
			if g.initOp == opF64Const {
				var raw [8]byte
				binary.LittleEndian.PutUint64(raw[:], uint64(g.initVal))
				p = append(p, raw[:]...)
			} else {
				p = appendSLEB128(p, g.initVal)
			}
			p = append(p, opEnd)
		}
		out = appendSection(out, 6, p)
	}

	// 7. Export section
	if len(m.exports) > 0 {
		var p []byte
		p = appendULEB128(p, uint32(len(m.exports)))
		for _, ex := range m.exports {
			p = appendWasmString(p, ex.name)
			p = append(p, ex.kind)
			p = appendULEB128(p, ex.idx)
		}
		out = appendSection(out, 7, p)
	}

	// 10. Code section
	if len(m.funcs) > 0 {
		var p []byte
		p = appendULEB128(p, uint32(len(m.funcs)))
		for _, fn := range m.funcs {
			var body []byte
			// Group consecutive locals of identical type
			type localRun struct {
				count uint32
				t     byte
			}
			var runs []localRun
			for _, lt := range fn.locals {
				if len(runs) > 0 && runs[len(runs)-1].t == lt {
					runs[len(runs)-1].count++
				} else {
					runs = append(runs, localRun{count: 1, t: lt})
				}
			}
			body = appendULEB128(body, uint32(len(runs)))
			for _, r := range runs {
				body = appendULEB128(body, r.count)
				body = append(body, r.t)
			}
			body = append(body, fn.code...)
			body = append(body, opEnd)
			p = appendULEB128(p, uint32(len(body)))
			p = append(p, body...)
		}
		out = appendSection(out, 10, p)
	}

	// 11. Data section
	var segs []wasmDataSeg
	if len(m.staticData) > 0 {
		segs = append(segs, wasmDataSeg{offset: m.staticBase, data: m.staticData})
	}
	segs = append(segs, m.data...)
	if len(segs) > 0 {
		var p []byte
		p = appendULEB128(p, uint32(len(segs)))
		for _, seg := range segs {
			p = append(p, 0x00) // active memory 0
			p = append(p, opI32Const)
			p = appendSLEB128(p, int64(seg.offset))
			p = append(p, opEnd)
			p = appendULEB128(p, uint32(len(seg.data)))
			p = append(p, seg.data...)
		}
		out = appendSection(out, 11, p)
	}

	return out
}
