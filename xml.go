// Copyright 2012-2016, 2018-2019 Charles Banning. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file

// xml.go - basically the core of X2j for map[string]interface{} values.
//          NewMapXml, NewMapXmlReader, mv.Xml, mv.XmlWriter
// see x2j and j2x for wrappers to provide end-to-end transformation of XML and JSON messages.

package mxj

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	textK      = "$"
	seqK       = "#seq"
	commentK   = "#comment"
	attrK      = "#attr"
	directiveK = "#directive"
	procinstK  = "#procinst"
	targetK    = "#target"
	instK      = "#inst"
)

// Support overriding default Map keys prefix

func SetGlobalKeyMapPrefix(s string) {
	textK = strings.ReplaceAll(textK, textK[0:1], s)
	seqK = strings.ReplaceAll(seqK, seqK[0:1], s)
	commentK = strings.ReplaceAll(commentK, commentK[0:1], s)
	directiveK = strings.ReplaceAll(directiveK, directiveK[0:1], s)
	procinstK = strings.ReplaceAll(procinstK, procinstK[0:1], s)
	targetK = strings.ReplaceAll(targetK, targetK[0:1], s)
	instK = strings.ReplaceAll(instK, instK[0:1], s)
	attrK = strings.ReplaceAll(attrK, attrK[0:1], s)
}

// ------------------- NewMapXml & NewMapXmlReader ... -------------------------

// If XmlCharsetReader != nil, it will be used to decode the XML, if required.
// Note: if CustomDecoder != nil, then XmlCharsetReader is ignored;
// set the CustomDecoder attribute instead.
//
//	  import (
//		     charset "code.google.com/p/go-charset/charset"
//		     github.com/clbanning/mxj
//		 )
//	  ...
//	  mxj.XmlCharsetReader = charset.NewReader
//	  m, merr := mxj.NewMapXml(xmlValue)
var XmlCharsetReader func(charset string, input io.Reader) (io.Reader, error)

// NewMapXml - convert a XML doc into a Map
// (This is analogous to unmarshalling a JSON string to map[string]interface{} using json.Unmarshal().)
//
//	If the optional argument 'cast' is 'true', then values will be converted to boolean or float64 if possible.
//
//	Converting XML to JSON is a simple as:
//		...
//		mapVal, merr := mxj.NewMapXml(xmlVal)
//		if merr != nil {
//			// handle error
//		}
//		jsonVal, jerr := mapVal.Json()
//		if jerr != nil {
//			// handle error
//		}
//
//	NOTES:
//	   1. Declarations, directives, process instructions and comments are NOT parsed.
//	   2. The 'xmlVal' will be parsed looking for an xml.StartElement, so BOM and other
//	      extraneous xml.CharData will be ignored unless io.EOF is reached first.
//	   3. If CoerceKeysToLower() has been called, then all key values will be lower case.
//	   4. If CoerceKeysToSnakeCase() has been called, then all key values will be converted to snake case.
//	   5. If DisableTrimWhiteSpace(b bool) has been called, then all values will be trimmed or not. 'true' by default.
func NewMapXml(xmlVal []byte, cast ...bool) (Map, error) {
	var r bool
	if len(cast) == 1 {
		r = cast[0]
	}
	return xmlToMap(xmlVal, r)
}

// Get next XML doc from an io.Reader as a Map value.  Returns Map value.
//
//	NOTES:
//	   1. Declarations, directives, process instructions and comments are NOT parsed.
//	   2. The 'xmlReader' will be parsed looking for an xml.StartElement, so BOM and other
//	      extraneous xml.CharData will be ignored unless io.EOF is reached first.
//	   3. If CoerceKeysToLower() has been called, then all key values will be lower case.
//	   4. If CoerceKeysToSnakeCase() has been called, then all key values will be converted to snake case.
func NewMapXmlReader(xmlReader io.Reader, cast ...bool) (Map, error) {
	var r bool
	if len(cast) == 1 {
		r = cast[0]
	}

	// We need to put an *os.File reader in a ByteReader or the xml.NewDecoder
	// will wrap it in a bufio.Reader and seek on the file beyond where the
	// xml.Decoder parses!
	if _, ok := xmlReader.(io.ByteReader); !ok {
		xmlReader = myByteReader(xmlReader) // see code at EOF
	}

	// build the map
	return xmlReaderToMap(xmlReader, r)
}

// Get next XML doc from an io.Reader as a Map value.  Returns Map value and slice with the raw XML.
//
//	NOTES:
//	   1. Declarations, directives, process instructions and comments are NOT parsed.
//	   2. Due to the implementation of xml.Decoder, the raw XML off the reader is buffered to []byte
//	      using a ByteReader. If the io.Reader is an os.File, there may be significant performance impact.
//	      See the examples - getmetrics1.go through getmetrics4.go - for comparative use cases on a large
//	      data set. If the io.Reader is wrapping a []byte value in-memory, however, such as http.Request.Body
//	      you CAN use it to efficiently unmarshal a XML doc and retrieve the raw XML in a single call.
//	   3. The 'raw' return value may be larger than the XML text value.
//	   4. The 'xmlReader' will be parsed looking for an xml.StartElement, so BOM and other
//	      extraneous xml.CharData will be ignored unless io.EOF is reached first.
//	   5. If CoerceKeysToLower() has been called, then all key values will be lower case.
//	   6. If CoerceKeysToSnakeCase() has been called, then all key values will be converted to snake case.
func NewMapXmlReaderRaw(xmlReader io.Reader, cast ...bool) (Map, []byte, error) {
	var r bool
	if len(cast) == 1 {
		r = cast[0]
	}
	// create TeeReader so we can retrieve raw XML
	buf := make([]byte, 0)
	wb := bytes.NewBuffer(buf)
	trdr := myTeeReader(xmlReader, wb) // see code at EOF

	m, err := xmlReaderToMap(trdr, r)

	// retrieve the raw XML that was decoded
	b := wb.Bytes()

	if err != nil {
		return nil, b, err
	}

	return m, b, nil
}

// xmlReaderToMap() - parse a XML io.Reader to a map[string]interface{} value
func xmlReaderToMap(rdr io.Reader, r bool) (map[string]interface{}, error) {
	// parse the Reader
	p := xml.NewDecoder(rdr)
	if CustomDecoder != nil {
		useCustomDecoder(p)
	} else {
		p.CharsetReader = XmlCharsetReader
	}
	return xmlToMapParser("", nil, p, r)
}

// xmlToMap - convert a XML doc into map[string]interface{} value
func xmlToMap(doc []byte, r bool) (map[string]interface{}, error) {
	b := bytes.NewReader(doc)
	p := xml.NewDecoder(b)
	if CustomDecoder != nil {
		useCustomDecoder(p)
	} else {
		p.CharsetReader = XmlCharsetReader
	}
	return xmlToMapParser("", nil, p, r)
}

// ===================================== where the work happens =============================

// PrependAttrWithHyphen. Prepend attribute tags with a hyphen.
// Default is 'true'. (Not applicable to NewMapXmlSeq(), mv.XmlSeq(), etc.)
//
//	Note:
//		If 'false', unmarshaling and marshaling is not symmetric. Attributes will be
//		marshal'd as <attr_tag>attr</attr_tag> and may be part of a list.
func PrependAttrWithHyphen(v bool) {
	if v {
		attrPrefix = "-"
		lenAttrPrefix = len(attrPrefix)
		return
	}
	attrPrefix = ""
	lenAttrPrefix = len(attrPrefix)
}

// Include sequence id with inner tags. - per Sean Murphy, murphysean84@gmail.com.
var includeTagSeqNum bool

// IncludeTagSeqNum - include a "_seq":N key:value pair with each inner tag, denoting
// its position when parsed. This is of limited usefulness, since list values cannot
// be tagged with "_seq" without changing their depth in the Map.
// So THIS SHOULD BE USED WITH CAUTION - see the test cases. Here's a sample of what
// you get.
/*
		<Obj c="la" x="dee" h="da">
			<IntObj id="3"/>
			<IntObj1 id="1"/>
			<IntObj id="2"/>
			<StrObj>hello</StrObj>
		</Obj>

	parses as:

		{
		Obj:{
			"-c":"la",
			"-h":"da",
			"-x":"dee",
			"intObj":[
				{
					"-id"="3",
					"_seq":"0" // if mxj.Cast is passed, then: "_seq":0
				},
				{
					"-id"="2",
					"_seq":"2"
				}],
			"intObj1":{
				"-id":"1",
				"_seq":"1"
				},
			"StrObj":{
				"#text":"hello", // simple element value gets "#text" tag
				"_seq":"3"
				}
			}
		}
*/
func IncludeTagSeqNum(b ...bool) {
	if len(b) == 0 {
		includeTagSeqNum = !includeTagSeqNum
	} else if len(b) == 1 {
		includeTagSeqNum = b[0]
	}
}

// all keys will be "lower case"
var lowerCase bool

// Coerce all tag values to keys in lower case.  This is useful if you've got sources with variable
// tag capitalization, and you want to use m.ValuesForKeys(), etc., with the key or path spec
// in lower case.
//
//	CoerceKeysToLower() will toggle the coercion flag true|false - on|off
//	CoerceKeysToLower(true|false) will set the coercion flag on|off
//
//	NOTE: only recognized by NewMapXml, NewMapXmlReader, and NewMapXmlReaderRaw functions as well as
//	      the associated HandleXmlReader and HandleXmlReaderRaw.
func CoerceKeysToLower(b ...bool) {
	if len(b) == 0 {
		lowerCase = !lowerCase
	} else if len(b) == 1 {
		lowerCase = b[0]
	}
}

// disableTrimWhiteSpace sets if the white space should be removed or not
var disableTrimWhiteSpace bool
var trimRunes = "\t\r\b\n "

// DisableTrimWhiteSpace set if the white space should be trimmed or not. By default white space is always trimmed. If
// no argument is provided, trim white space will be disabled.
func DisableTrimWhiteSpace(b ...bool) {
	if len(b) == 0 {
		disableTrimWhiteSpace = true
	} else {
		disableTrimWhiteSpace = b[0]
	}

	if disableTrimWhiteSpace {
		trimRunes = "\t\r\b\n"
	} else {
		trimRunes = "\t\r\b\n "
	}
}

// 25jun16: Allow user to specify the "prefix" character for XML attribute key labels.
// We do this by replacing '`' constant with attrPrefix var, replacing useHyphen with attrPrefix = "",
// and adding a SetAttrPrefix(s string) function.

var attrPrefix string = `-` // the default
var lenAttrPrefix int = 1   // the default

// SetAttrPrefix changes the default, "-", to the specified value, s.
// SetAttrPrefix("") is the same as PrependAttrWithHyphen(false).
// (Not applicable for NewMapXmlSeq(), mv.XmlSeq(), etc.)
func SetAttrPrefix(s string) {
	attrPrefix = s
	lenAttrPrefix = len(attrPrefix)
}

// 18jan17: Allows user to specify if the map keys should be in snake case instead
// of the default hyphenated notation.
var snakeCaseKeys bool

// CoerceKeysToSnakeCase changes the default, false, to the specified value, b.
// Note: the attribute prefix will be a hyphen, '-', or what ever string value has
// been specified using SetAttrPrefix.
func CoerceKeysToSnakeCase(b ...bool) {
	if len(b) == 0 {
		snakeCaseKeys = !snakeCaseKeys
	} else if len(b) == 1 {
		snakeCaseKeys = b[0]
	}
}

// 10jan19: use of pull request #57 should be conditional - legacy code assumes
// numeric values are float64.
var castToInt bool

// CastValuesToInt tries to coerce numeric valus to int64 or uint64 instead of the
// default float64. Repeated calls with no argument will toggle this on/off, or this
// handling will be set with the value of 'b'.
func CastValuesToInt(b ...bool) {
	if len(b) == 0 {
		castToInt = !castToInt
	} else if len(b) == 1 {
		castToInt = b[0]
	}
}

// 05feb17: support processing XMPP streams (issue #36)
var handleXMPPStreamTag bool

// HandleXMPPStreamTag causes decoder to parse XMPP <stream:stream> elements.
// If called with no argument, XMPP stream element handling is toggled on/off.
// (See xmppStream_test.go for example.)
//
//	If called with NewMapXml, NewMapXmlReader, New MapXmlReaderRaw the "stream"
//	element will be  returned as:
//		map["stream"]interface{}{map[-<attrs>]interface{}}.
//	If called with NewMapSeq, NewMapSeqReader, NewMapSeqReaderRaw the "stream"
//	element will be returned as:
//		map["stream:stream"]interface{}{map["#attr"]interface{}{map[string]interface{}}}
//		where the "#attr" values have "#text" and "#seq" keys. (See NewMapXmlSeq.)
func HandleXMPPStreamTag(b ...bool) {
	if len(b) == 0 {
		handleXMPPStreamTag = !handleXMPPStreamTag
	} else if len(b) == 1 {
		handleXMPPStreamTag = b[0]
	}
}

// 21jan18 - decode all values as map["#text":value] (issue #56)
var decodeSimpleValuesAsMap bool

// DecodeSimpleValuesAsMap forces all values to be decoded as map["#text":<value>].
// If called with no argument, the decoding is toggled on/off.
//
// By default the NewMapXml functions decode simple values without attributes as
// map[<tag>:<value>]. This function causes simple values without attributes to be
// decoded the same as simple values with attributes - map[<tag>:map["#text":<value>]].
func DecodeSimpleValuesAsMap(b ...bool) {
	if len(b) == 0 {
		decodeSimpleValuesAsMap = !decodeSimpleValuesAsMap
	} else if len(b) == 1 {
		decodeSimpleValuesAsMap = b[0]
	}
}

// xmlToMapParser (2015.11.12) - load a 'clean' XML doc into a map[string]interface{} directly.
// A refactoring of xmlToTreeParser(), markDuplicate() and treeToMap() - here, all-in-one.
// We've removed the intermediate *node tree with the allocation and subsequent rescanning.
func xmlToMapParser(skey string, a []xml.Attr, p *xml.Decoder, r bool) (map[string]interface{}, error) {
	if lowerCase {
		skey = strings.ToLower(skey)
	}
	if snakeCaseKeys {
		skey = strings.Replace(skey, "-", "_", -1)
	}

	// NOTE: all attributes and sub-elements parsed into 'na', 'na' is returned as value for 'skey' in 'n'.
	// Unless 'skey' is a simple element w/o attributes, in which case the xml.CharData value is the value.
	var n, na map[string]interface{}
	var seq int // for includeTagSeqNum

	// Allocate maps and load attributes, if any.
	// NOTE: on entry from NewMapXml(), etc., skey=="", and we fall through
	//       to get StartElement then recurse with skey==xml.StartElement.Name.Local
	//       where we begin allocating map[string]interface{} values 'n' and 'na'.
	if skey != "" {
		n = make(map[string]interface{})  // old n
		na = make(map[string]interface{}) // old n.nodes
		if len(a) > 0 {
			for _, v := range a {
				if snakeCaseKeys {
					v.Name.Local = strings.Replace(v.Name.Local, "-", "_", -1)
				}
				var key string
				key = attrPrefix + v.Name.Local
				if lowerCase {
					key = strings.ToLower(key)
				}
				if xmlEscapeCharsDecoder { // per issue#84
					v.Value = escapeChars(v.Value)
				}
				na[key] = cast(v.Value, r, key)
			}
		}
	}
	// Return XMPP <stream:stream> message.
	if handleXMPPStreamTag && skey == "stream" {
		n[skey] = na
		return n, nil
	}

	for {
		t, err := p.Token()
		if err != nil {
			if err != io.EOF {
				return nil, errors.New("xml.Decoder.Token() - " + err.Error())
			}
			return nil, err
		}
		switch t.(type) {
		case xml.StartElement:
			tt := t.(xml.StartElement)

			// First call to xmlToMapParser() doesn't pass xml.StartElement - the map key.
			// So when the loop is first entered, the first token is the root tag along
			// with any attributes, which we process here.
			//
			// Subsequent calls to xmlToMapParser() will pass in tag+attributes for
			// processing before getting the next token which is the element value,
			// which is done above.
			if skey == "" {
				return xmlToMapParser(tt.Name.Local, tt.Attr, p, r)
			}

			// If not initializing the map, parse the element.
			// len(nn) == 1, necessarily - it is just an 'n'.
			nn, err := xmlToMapParser(tt.Name.Local, tt.Attr, p, r)
			if err != nil {
				return nil, err
			}

			// The nn map[string]interface{} value is a na[nn_key] value.
			// We need to see if nn_key already exists - means we're parsing a list.
			// This may require converting na[nn_key] value into []interface{} type.
			// First, extract the key:val for the map - it's a singleton.
			// Note:
			// * if CoerceKeysToLower() called, then key will be lower case.
			// * if CoerceKeysToSnakeCase() called, then key will be converted to snake case.
			var key string
			var val interface{}
			for key, val = range nn {
				break
			}

			// IncludeTagSeqNum requests that the element be augmented with a "_seq" sub-element.
			// In theory, we don't need this if len(na) == 1. But, we don't know what might
			// come next - we're only parsing forward.  So if you ask for 'includeTagSeqNum' you
			// get it on every element. (Personally, I never liked this, but I added it on request
			// and did get a $50 Amazon gift card in return - now we support it for backwards compatibility!)
			if includeTagSeqNum {
				switch val.(type) {
				case []interface{}:
					// noop - There's no clean way to handle this w/o changing message structure.
				case map[string]interface{}:
					val.(map[string]interface{})["_seq"] = seq // will overwrite an "_seq" XML tag
					seq++
				case interface{}: // a non-nil simple element: string, float64, bool
					v := map[string]interface{}{textK: val}
					v["_seq"] = seq
					seq++
					val = v
				}
			}

			// 'na' holding sub-elements of n.
			// See if 'key' already exists.
			// If 'key' exists, then this is a list, if not just add key:val to na.
			if v, ok := na[key]; ok {
				var a []interface{}
				switch v.(type) {
				case []interface{}:
					a = v.([]interface{})
				default: // anything else - note: v.(type) != nil
					a = []interface{}{v}
				}
				a = append(a, val)
				na[key] = a
			} else {
				na[key] = val // save it as a singleton
			}
		case xml.EndElement:
			// len(n) > 0 if this is a simple element w/o xml.Attrs - see xml.CharData case.
			if len(n) == 0 {
				// If len(na)==0 we have an empty element == "";
				// it has no xml.Attr nor xml.CharData.
				// Note: in original node-tree parser, val defaulted to "";
				// so we always had the default if len(node.nodes) == 0.
				if len(na) > 0 {
					n[skey] = na
				} else {
					n[skey] = "" // empty element
				}
			} else if len(n) == 1 && len(na) > 0 {
				// it's a simple element w/ no attributes w/ subelements
				for _, v := range n {
					na[textK] = v
				}
				n[skey] = na
			}
			return n, nil
		case xml.CharData:
			// clean up possible noise
			tt := strings.Trim(string(t.(xml.CharData)), trimRunes)
			if xmlEscapeCharsDecoder { // issue#84
				tt = escapeChars(tt)
			}
			if len(tt) > 0 {
				if len(na) > 0 || decodeSimpleValuesAsMap {
					na[textK] = cast(tt, r, textK)
				} else if skey != "" {
					n[skey] = cast(tt, r, skey)
				} else {
					// per Adrian (http://www.adrianlungu.com/) catch stray text
					// in decoder stream -
					// https://github.com/clbanning/mxj/pull/14#issuecomment-182816374
					// NOTE: CharSetReader must be set to non-UTF-8 CharSet or you'll get
					// a p.Token() decoding error when the BOM is UTF-16 or UTF-32.
					continue
				}
			}
		default:
			// noop
		}
	}
}

var castNanInf bool

// Cast "Nan", "Inf", "-Inf" XML values to 'float64'.
// By default, these values will be decoded as 'string'.
func CastNanInf(b ...bool) {
	if len(b) == 0 {
		castNanInf = !castNanInf
	} else if len(b) == 1 {
		castNanInf = b[0]
	}
}

// cast - try to cast string values to bool or float64
// 't' is the tag key that can be checked for 'not-casting'
func cast(s string, r bool, t string) interface{} {
	if checkTagToSkip != nil && t != "" && checkTagToSkip(t) {
		// call the check-function here with 't[0]'
		// if 'true' return s
		return s
	}

	if r {
		// handle nan and inf
		if !castNanInf {
			switch strings.ToLower(s) {
			case "nan", "inf", "-inf":
				return s
			}
		}

		// handle numeric strings ahead of boolean
		if castToInt {
			if f, err := strconv.ParseInt(s, 10, 64); err == nil {
				return f
			}
			if f, err := strconv.ParseUint(s, 10, 64); err == nil {
				return f
			}
		}

		if castToFloat {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return f
			}
		}

		// ParseBool treats "1"==true & "0"==false, we've already scanned those
		// values as float64. See if value has 't' or 'f' as initial screen to
		// minimize calls to ParseBool; also, see if len(s) < 6.
		if castToBool {
			if len(s) > 0 && len(s) < 6 {
				switch s[:1] {
				case "t", "T", "f", "F":
					if b, err := strconv.ParseBool(s); err == nil {
						return b
					}
				}
			}
		}
	}
	return s
}

// pull request, #59
var castToFloat = true

// CastValuesToFloat can be used to skip casting to float64 when
// "cast" argument is 'true' in NewMapXml, etc.
// Default is true.
func CastValuesToFloat(b ...bool) {
	if len(b) == 0 {
		castToFloat = !castToFloat
	} else if len(b) == 1 {
		castToFloat = b[0]
	}
}

var castToBool = true

// CastValuesToBool can be used to skip casting to bool when
// "cast" argument is 'true' in NewMapXml, etc.
// Default is true.
func CastValuesToBool(b ...bool) {
	if len(b) == 0 {
		castToBool = !castToBool
	} else if len(b) == 1 {
		castToBool = b[0]
	}
}

// checkTagToSkip - switch to address Issue #58

var checkTagToSkip func(string) bool

// SetCheckTagToSkipFunc registers function to test whether the value
// for a tag should be cast to bool or float64 when "cast" argument is 'true'.
// (Dot tag path notation is not supported.)
// NOTE: key may be "#text" if it's a simple element with attributes
//
//	or "decodeSimpleValuesAsMap == true".
//
// NOTE: does not apply to NewMapXmlSeq... functions.
func SetCheckTagToSkipFunc(fn func(string) bool) {
	checkTagToSkip = fn
}

// ------------------ END: NewMapXml & NewMapXmlReader -------------------------

// ------------------ mv.Xml & mv.XmlWriter - from j2x ------------------------

const (
	DefaultRootTag = "doc"
)

var useGoXmlEmptyElemSyntax bool

// XmlGoEmptyElemSyntax() - <tag ...></tag> rather than <tag .../>.
//
//	Go's encoding/xml package marshals empty XML elements as <tag ...></tag>.  By default this package
//	encodes empty elements as <tag .../>.  If you're marshaling Map values that include structures
//	(which are passed to xml.Marshal for encoding), this will let you conform to the standard package.
func XmlGoEmptyElemSyntax() {
	useGoXmlEmptyElemSyntax = true
}

// XmlDefaultEmptyElemSyntax() - <tag .../> rather than <tag ...></tag>.
// Return XML encoding for empty elements to the default package setting.
// Reverses effect of XmlGoEmptyElemSyntax().
func XmlDefaultEmptyElemSyntax() {
	useGoXmlEmptyElemSyntax = false
}

// ------- issue #88 ----------
// xmlCheckIsValid set switch to force decoding the encoded XML to
// see if it is valid XML.
var xmlCheckIsValid bool

// XmlCheckIsValid forces the encoded XML to be checked for validity.
func XmlCheckIsValid(b ...bool) {
	if len(b) == 1 {
		xmlCheckIsValid = b[0]
		return
	}
	xmlCheckIsValid = !xmlCheckIsValid
}

// Encode a Map as XML.  The companion of NewMapXml().
// The following rules apply.
//   - The key label "#text" is treated as the value for a simple element with attributes.
//   - Map keys that begin with a hyphen, '-', are interpreted as attributes.
//     It is an error if the attribute doesn't have a []byte, string, number, or boolean value.
//   - Map value type encoding:
//     > string, bool, float64, int, int32, int64, float32: per "%v" formating
//     > []bool, []uint8: by casting to string
//     > structures, etc.: handed to xml.Marshal() - if there is an error, the element
//     value is "UNKNOWN"
//   - Elements with only attribute values or are null are terminated using "/>".
//   - If len(mv) == 1 and no rootTag is provided, then the map key is used as the root tag, possible.
//     Thus, `{ "key":"value" }` encodes as "<key>value</key>".
//   - To encode empty elements in a syntax consistent with encoding/xml call UseGoXmlEmptyElementSyntax().
//
// The attributes tag=value pairs are alphabetized by "tag".  Also, when encoding map[string]interface{} values -
// complex elements, etc. - the key:value pairs are alphabetized by key so the resulting tags will appear sorted.
func (mv Map) Xml(rootTag ...string) ([]byte, error) {
	m := map[string]interface{}(mv)
	var err error
	b := new(bytes.Buffer)
	p := new(pretty) // just a stub

	if len(m) == 1 && len(rootTag) == 0 {
		for key, value := range m {
			// if it an array, see if all values are map[string]interface{}
			// we force a new root tag if we'll end up with no key:value in the list
			// so: key:[string_val, bool:true] --> <doc><key>string_val</key><bool>true</bool></doc>
			switch value.(type) {
			case []interface{}:
				for _, v := range value.([]interface{}) {
					switch v.(type) {
					case map[string]interface{}: // noop
					default: // anything else
						err = marshalMapToXmlIndent(false, b, DefaultRootTag, m, p)
						goto done
					}
				}
			}
			err = marshalMapToXmlIndent(false, b, key, value, p)
		}
	} else if len(rootTag) == 1 {
		err = marshalMapToXmlIndent(false, b, rootTag[0], m, p)
	} else {
		err = marshalMapToXmlIndent(false, b, DefaultRootTag, m, p)
	}
done:
	if xmlCheckIsValid {
		d := xml.NewDecoder(bytes.NewReader(b.Bytes()))
		for {
			_, err = d.Token()
			if err == io.EOF {
				err = nil
				break
			} else if err != nil {
				return nil, err
			}
		}
	}
	return b.Bytes(), err
}

// The following implementation is provided only for symmetry with NewMapXmlReader[Raw]
// The names will also provide a key for the number of return arguments.

// Writes the Map as  XML on the Writer.
// See Xml() for encoding rules.
func (mv Map) XmlWriter(xmlWriter io.Writer, rootTag ...string) error {
	x, err := mv.Xml(rootTag...)
	if err != nil {
		return err
	}

	_, err = xmlWriter.Write(x)
	return err
}

// Writes the Map as  XML on the Writer. []byte is the raw XML that was written.
// See Xml() for encoding rules.
/*
func (mv Map) XmlWriterRaw(xmlWriter io.Writer, rootTag ...string) ([]byte, error) {
	x, err := mv.Xml(rootTag...)
	if err != nil {
		return x, err
	}

	_, err = xmlWriter.Write(x)
	return x, err
}
*/

// Writes the Map as pretty XML on the Writer.
// See Xml() for encoding rules.
func (mv Map) XmlIndentWriter(xmlWriter io.Writer, prefix, indent string, rootTag ...string) error {
	x, err := mv.XmlIndent(prefix, indent, rootTag...)
	if err != nil {
		return err
	}

	_, err = xmlWriter.Write(x)
	return err
}

// Writes the Map as pretty XML on the Writer. []byte is the raw XML that was written.
// See Xml() for encoding rules.
/*
func (mv Map) XmlIndentWriterRaw(xmlWriter io.Writer, prefix, indent string, rootTag ...string) ([]byte, error) {
	x, err := mv.XmlIndent(prefix, indent, rootTag...)
	if err != nil {
		return x, err
	}

	_, err = xmlWriter.Write(x)
	return x, err
}
*/

// -------------------- END: mv.Xml & mv.XmlWriter -------------------------------

// --------------  Handle XML stream by processing Map value --------------------

// Default poll delay to keep Handler from spinning on an open stream
// like sitting on os.Stdin waiting for imput.
var xhandlerPollInterval = time.Millisecond

// Bulk process XML using handlers that process a Map value.
//
//	'rdr' is an io.Reader for XML (stream)
//	'mapHandler' is the Map processor. Return of 'false' stops io.Reader processing.
//	'errHandler' is the error processor. Return of 'false' stops io.Reader processing and returns the error.
//	Note: mapHandler() and errHandler() calls are blocking, so reading and processing of messages is serialized.
//	      This means that you can stop reading the file on error or after processing a particular message.
//	      To have reading and handling run concurrently, pass argument to a go routine in handler and return 'true'.
func HandleXmlReader(xmlReader io.Reader, mapHandler func(Map) bool, errHandler func(error) bool) error {
	var n int
	for {
		m, merr := NewMapXmlReader(xmlReader)
		n++

		// handle error condition with errhandler
		if merr != nil && merr != io.EOF {
			merr = fmt.Errorf("[xmlReader: %d] %s", n, merr.Error())
			if ok := errHandler(merr); !ok {
				// caused reader termination
				return merr
			}
			continue
		}

		// pass to maphandler
		if len(m) != 0 {
			if ok := mapHandler(m); !ok {
				break
			}
		} else if merr != io.EOF {
			time.Sleep(xhandlerPollInterval)
		}

		if merr == io.EOF {
			break
		}
	}
	return nil
}

// Bulk process XML using handlers that process a Map value and the raw XML.
//
//	'rdr' is an io.Reader for XML (stream)
//	'mapHandler' is the Map and raw XML - []byte - processor. Return of 'false' stops io.Reader processing.
//	'errHandler' is the error and raw XML processor. Return of 'false' stops io.Reader processing and returns the error.
//	Note: mapHandler() and errHandler() calls are blocking, so reading and processing of messages is serialized.
//	      This means that you can stop reading the file on error or after processing a particular message.
//	      To have reading and handling run concurrently, pass argument(s) to a go routine in handler and return 'true'.
//	See NewMapXmlReaderRaw for comment on performance associated with retrieving raw XML from a Reader.
func HandleXmlReaderRaw(xmlReader io.Reader, mapHandler func(Map, []byte) bool, errHandler func(error, []byte) bool) error {
	var n int
	for {
		m, raw, merr := NewMapXmlReaderRaw(xmlReader)
		n++

		// handle error condition with errhandler
		if merr != nil && merr != io.EOF {
			merr = fmt.Errorf("[xmlReader: %d] %s", n, merr.Error())
			if ok := errHandler(merr, raw); !ok {
				// caused reader termination
				return merr
			}
			continue
		}

		// pass to maphandler
		if len(m) != 0 {
			if ok := mapHandler(m, raw); !ok {
				break
			}
		} else if merr != io.EOF {
			time.Sleep(xhandlerPollInterval)
		}

		if merr == io.EOF {
			break
		}
	}
	return nil
}

// ----------------- END: Handle XML stream by processing Map value --------------

// --------  a hack of io.TeeReader ... need one that's an io.ByteReader for xml.NewDecoder() ----------

// This is a clone of io.TeeReader with the additional method t.ReadByte().
// Thus, this TeeReader is also an io.ByteReader.
// This is necessary because xml.NewDecoder uses a ByteReader not a Reader. It appears to have been written
// with bufio.Reader or bytes.Reader in mind ... not a generic io.Reader, which doesn't have to have ReadByte()..
// If NewDecoder is passed a Reader that does not satisfy ByteReader() it wraps the Reader with
// bufio.NewReader and uses ReadByte rather than Read that runs the TeeReader pipe logic.

type teeReader struct {
	r io.Reader
	w io.Writer
	b []byte
}

func myTeeReader(r io.Reader, w io.Writer) io.Reader {
	b := make([]byte, 1)
	return &teeReader{r, w, b}
}

// need for io.Reader - but we don't use it ...
func (t *teeReader) Read(p []byte) (int, error) {
	return 0, nil
}

func (t *teeReader) ReadByte() (byte, error) {
	n, err := t.r.Read(t.b)
	if n > 0 {
		if _, err := t.w.Write(t.b[:1]); err != nil {
			return t.b[0], err
		}
	}
	return t.b[0], err
}

// For use with NewMapXmlReader & NewMapXmlSeqReader.
type byteReader struct {
	r io.Reader
	b []byte
}

func myByteReader(r io.Reader) io.Reader {
	b := make([]byte, 1)
	return &byteReader{r, b}
}

// Need for io.Reader interface ...
// Needed if reading a malformed http.Request.Body - issue #38.
func (b *byteReader) Read(p []byte) (int, error) {
	return b.r.Read(p)
}

func (b *byteReader) ReadByte() (byte, error) {
	_, err := b.r.Read(b.b)
	if len(b.b) > 0 {
		// issue #38
		return b.b[0], err
	}
	var c byte
	return c, err
}

// ----------------------- END: io.TeeReader hack -----------------------------------

// ---------------------- XmlIndent - from j2x package ----------------------------

// Encode a map[string]interface{} as a pretty XML string.
// See Xml for encoding rules.
func (mv Map) XmlIndent(prefix, indent string, rootTag ...string) ([]byte, error) {
	m := map[string]interface{}(mv)

	var err error
	b := new(bytes.Buffer)
	p := new(pretty)
	p.indent = indent
	p.padding = prefix

	if len(m) == 1 && len(rootTag) == 0 {
		// this can extract the key for the single map element
		// use it if it isn't a key for a list
		for key, value := range m {
			if _, ok := value.([]interface{}); ok {
				err = marshalMapToXmlIndent(true, b, DefaultRootTag, m, p)
			} else {
				err = marshalMapToXmlIndent(true, b, key, value, p)
			}
		}
	} else if len(rootTag) == 1 {
		err = marshalMapToXmlIndent(true, b, rootTag[0], m, p)
	} else {
		err = marshalMapToXmlIndent(true, b, DefaultRootTag, m, p)
	}
	if xmlCheckIsValid {
		d := xml.NewDecoder(bytes.NewReader(b.Bytes()))
		for {
			_, err = d.Token()
			if err == io.EOF {
				err = nil
				break
			} else if err != nil {
				return nil, err
			}
		}
	}
	return b.Bytes(), err
}

type pretty struct {
	indent   string
	cnt      int
	padding  string
	mapDepth int
	start    int
}

func (p *pretty) Indent() {
	p.padding += p.indent
	p.cnt++
}

func (p *pretty) Outdent() {
	if p.cnt > 0 {
		p.padding = p.padding[:len(p.padding)-len(p.indent)]
		p.cnt--
	}
}

// where the work actually happens
// returns an error if an attribute is not atomic
// NOTE: 01may20 - replaces mapToXmlIndent(); uses bytes.Buffer instead for string appends.
func marshalMapToXmlIndent(doIndent bool, b *bytes.Buffer, key string, value interface{}, pp *pretty) error {
	var err error
	var endTag bool
	var isSimple bool
	var elen int
	p := &pretty{pp.indent, pp.cnt, pp.padding, pp.mapDepth, pp.start}

	// per issue #48, 18apr18 - try and coerce maps to map[string]interface{}
	// Don't need for mapToXmlSeqIndent, since maps there are decoded by NewMapXmlSeq().
	if reflect.ValueOf(value).Kind() == reflect.Map {
		switch value.(type) {
		case map[string]interface{}:
		default:
			val := make(map[string]interface{})
			vv := reflect.ValueOf(value)
			keys := vv.MapKeys()
			for _, k := range keys {
				val[fmt.Sprint(k)] = vv.MapIndex(k).Interface()
			}
			value = val
		}
	}

	// 14jul20.  The following block of code has become something of a catch all for odd stuff
	// that might be passed in as a result of casting an arbitrary map[<T>]<T> to an mxj.Map
	// value and then call m.Xml or m.XmlIndent. See issue #71 (and #73) for such edge cases.
	switch value.(type) {
	// these types are handled during encoding
	case map[string]interface{}, []byte, string, float64, bool, int, int32, int64, float32, json.Number:
	case []map[string]interface{}, []string, []float64, []bool, []int, []int32, []int64, []float32, []json.Number:
	case []interface{}:
	case nil:
		value = ""
	default:
		// see if value is a struct, if so marshal using encoding/xml package
		if reflect.ValueOf(value).Kind() == reflect.Struct {
			if v, err := xml.Marshal(value); err != nil {
				return err
			} else {
				value = string(v)
			}
		} else {
			// coerce eveything else into a string value
			value = fmt.Sprint(value)
		}
	}

	// start the XML tag with required indentaton and padding
	if doIndent {
		switch value.(type) {
		case []interface{}, []string:
			// list processing handles indentation for all elements
		default:
			if _, err = b.WriteString(p.padding); err != nil {
				return err
			}
		}
	}
	switch value.(type) {
	case []interface{}:
	default:
		if _, err = b.WriteString(`<` + key); err != nil {
			return err
		}
	}

	switch value.(type) {
	case map[string]interface{}:
		vv := value.(map[string]interface{})
		lenvv := len(vv)
		// scan out attributes - attribute keys have prepended attrPrefix
		attrlist := make([][2]string, len(vv))
		var n int
		var ss string
		for k, v := range vv {
			if lenAttrPrefix > 0 && lenAttrPrefix < len(k) && k[:lenAttrPrefix] == attrPrefix {
				switch v.(type) {
				case string:
					if xmlEscapeChars {
						ss = escapeChars(v.(string))
					} else {
						ss = v.(string)
					}
					attrlist[n][0] = k[lenAttrPrefix:]
					attrlist[n][1] = ss
				case float64, bool, int, int32, int64, float32, json.Number:
					attrlist[n][0] = k[lenAttrPrefix:]
					attrlist[n][1] = fmt.Sprintf("%v", v)
				case []byte:
					if xmlEscapeChars {
						ss = escapeChars(string(v.([]byte)))
					} else {
						ss = string(v.([]byte))
					}
					attrlist[n][0] = k[lenAttrPrefix:]
					attrlist[n][1] = ss
				default:
					return fmt.Errorf("invalid attribute value for: %s:<%T>", k, v)
				}
				n++
			}
		}
		if n > 0 {
			attrlist = attrlist[:n]
			sort.Sort(attrList(attrlist))
			for _, v := range attrlist {
				if _, err = b.WriteString(` ` + v[0] + `="` + v[1] + `"`); err != nil {
					return err
				}
			}
		}
		// only attributes?
		if n == lenvv {
			if useGoXmlEmptyElemSyntax {
				if _, err = b.WriteString(`</` + key + ">"); err != nil {
					return err
				}
			} else {
				if _, err = b.WriteString(`/>`); err != nil {
					return err
				}
			}
			break
		}

		// simple element? Note: '#text" is an invalid XML tag.
		isComplex := false
		if v, ok := vv[textK]; ok && n+1 == lenvv {
			// just the value and attributes
			switch v.(type) {
			case string:
				if xmlEscapeChars {
					v = escapeChars(v.(string))
				} else {
					v = v.(string)
				}
			case []byte:
				if xmlEscapeChars {
					v = escapeChars(string(v.([]byte)))
				} else {
					v = string(v.([]byte))
				}
			}
			if _, err = b.WriteString(">" + fmt.Sprintf("%v", v)); err != nil {
				return err
			}
			endTag = true
			elen = 1
			isSimple = true
			break
		} else if ok {
			// need to handle when there are subelements in addition to the simple element value
			// issue #90
			switch v.(type) {
			case string:
				if xmlEscapeChars {
					v = escapeChars(v.(string))
				} else {
					v = v.(string)
				}
			case []byte:
				if xmlEscapeChars {
					v = escapeChars(string(v.([]byte)))
				} else {
					v = string(v.([]byte))
				}
			}
			if _, err = b.WriteString(">" + fmt.Sprintf("%v", v)); err != nil {
				return err
			}
			isComplex = true
		}

		// close tag with possible attributes
		if !isComplex {
			if _, err = b.WriteString(">"); err != nil {
				return err
			}
		}
		if doIndent {
			// *s += "\n"
			if _, err = b.WriteString("\n"); err != nil {
				return err
			}
		}
		// something more complex
		p.mapDepth++
		// extract the map k:v pairs and sort on key
		elemlist := make([][2]interface{}, len(vv))
		n = 0
		for k, v := range vv {
			if k == textK {
				// simple element handled above
				continue
			}
			if lenAttrPrefix > 0 && lenAttrPrefix < len(k) && k[:lenAttrPrefix] == attrPrefix {
				continue
			}
			elemlist[n][0] = k
			elemlist[n][1] = v
			n++
		}
		elemlist = elemlist[:n]
		sort.Sort(elemList(elemlist))
		var i int
		for _, v := range elemlist {
			switch v[1].(type) {
			case []interface{}:
			default:
				if i == 0 && doIndent {
					p.Indent()
				}
			}
			i++
			if err := marshalMapToXmlIndent(doIndent, b, v[0].(string), v[1], p); err != nil {
				return err
			}
			switch v[1].(type) {
			case []interface{}: // handled in []interface{} case
			default:
				if doIndent {
					p.Outdent()
				}
			}
			i--
		}
		p.mapDepth--
		endTag = true
		elen = 1 // we do have some content ...
	case []interface{}:
		// special case - found during implementing Issue #23
		if len(value.([]interface{})) == 0 {
			if doIndent {
				if _, err = b.WriteString(p.padding + p.indent); err != nil {
					return err
				}
			}
			if _, err = b.WriteString("<" + key); err != nil {
				return err
			}
			elen = 0
			endTag = true
			break
		}
		for _, v := range value.([]interface{}) {
			if doIndent {
				p.Indent()
			}
			if err := marshalMapToXmlIndent(doIndent, b, key, v, p); err != nil {
				return err
			}
			if doIndent {
				p.Outdent()
			}
		}
		return nil
	case []string:
		// This was added by https://github.com/slotix ... not a type that
		// would be encountered if mv generated from NewMapXml, NewMapJson.
		// Could be encountered in AnyXml(), so we'll let it stay, though
		// it should be merged with case []interface{}, above.
		//quick fix for []string type
		//[]string should be treated exaclty as []interface{}
		if len(value.([]string)) == 0 {
			if doIndent {
				if _, err = b.WriteString(p.padding + p.indent); err != nil {
					return err
				}
			}
			if _, err = b.WriteString("<" + key); err != nil {
				return err
			}
			elen = 0
			endTag = true
			break
		}
		for _, v := range value.([]string) {
			if doIndent {
				p.Indent()
			}
			if err := marshalMapToXmlIndent(doIndent, b, key, v, p); err != nil {
				return err
			}
			if doIndent {
				p.Outdent()
			}
		}
		return nil
	case nil:
		// terminate the tag
		if doIndent {
			// *s += p.padding
			if _, err = b.WriteString(p.padding); err != nil {
				return err
			}
		}
		if _, err = b.WriteString("<" + key); err != nil {
			return err
		}
		endTag, isSimple = true, true
		break
	default: // handle anything - even goofy stuff
		elen = 0
		switch value.(type) {
		case string:
			v := value.(string)
			if xmlEscapeChars {
				v = escapeChars(v)
			}
			elen = len(v)
			if elen > 0 {
				// *s += ">" + v
				if _, err = b.WriteString(">" + v); err != nil {
					return err
				}
			}
		case float64, bool, int, int32, int64, float32, json.Number:
			v := fmt.Sprintf("%v", value)
			elen = len(v) // always > 0
			if _, err = b.WriteString(">" + v); err != nil {
				return err
			}
		case []byte: // NOTE: byte is just an alias for uint8
			// similar to how xml.Marshal handles []byte structure members
			v := string(value.([]byte))
			if xmlEscapeChars {
				v = escapeChars(v)
			}
			elen = len(v)
			if elen > 0 {
				// *s += ">" + v
				if _, err = b.WriteString(">" + v); err != nil {
					return err
				}
			}
		default:
			if _, err = b.WriteString(">"); err != nil {
				return err
			}
			var v []byte
			var err error
			if doIndent {
				v, err = xml.MarshalIndent(value, p.padding, p.indent)
			} else {
				v, err = xml.Marshal(value)
			}
			if err != nil {
				if _, err = b.WriteString(">UNKNOWN"); err != nil {
					return err
				}
			} else {
				elen = len(v)
				if elen > 0 {
					if _, err = b.Write(v); err != nil {
						return err
					}
				}
			}
		}
		isSimple = true
		endTag = true
	}
	if endTag {
		if doIndent {
			if !isSimple {
				if _, err = b.WriteString(p.padding); err != nil {
					return err
				}
			}
		}
		if elen > 0 || useGoXmlEmptyElemSyntax {
			if elen == 0 {
				if _, err = b.WriteString(">"); err != nil {
					return err
				}
			}
			if _, err = b.WriteString(`</` + key + ">"); err != nil {
				return err
			}
		} else {
			if _, err = b.WriteString(`/>`); err != nil {
				return err
			}
		}
	}
	if doIndent {
		if p.cnt > p.start {
			if _, err = b.WriteString("\n"); err != nil {
				return err
			}
		}
		p.Outdent()
	}

	return nil
}

// ============================ sort interface implementation =================

type attrList [][2]string

func (a attrList) Len() int {
	return len(a)
}

func (a attrList) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func (a attrList) Less(i, j int) bool {
	return a[i][0] <= a[j][0]
}

type elemList [][2]interface{}

func (e elemList) Len() int {
	return len(e)
}

func (e elemList) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

func (e elemList) Less(i, j int) bool {
	return e[i][0].(string) <= e[j][0].(string)
}
