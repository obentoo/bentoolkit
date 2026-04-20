package autoupdate

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestFallbackSuggestion tests Property 17: Fallback Suggestion
// **Feature: autoupdate-analyzer, Property 17: Fallback Suggestion**
// **Validates: Requirements 7.2, 7.3, 7.4**
//
// For any primary parser type, the LLM SHALL suggest at least one fallback
// parser of a different type.
func TestFallbackSuggestion(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: SuggestFallbacks returns at least one fallback for any primary parser
	properties.Property("SuggestFallbacks returns at least one fallback for any primary parser", prop.ForAll(
		func(primaryParser string) bool {
			fallbacks := SuggestFallbacks(primaryParser)
			return len(fallbacks) >= 1
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: All suggested fallbacks are different from primary parser
	properties.Property("All suggested fallbacks are different from primary parser", prop.ForAll(
		func(primaryParser string) bool {
			fallbacks := SuggestFallbacks(primaryParser)
			for _, fb := range fallbacks {
				if fb.ParserType == primaryParser {
					return false
				}
			}
			return true
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: JSON primary suggests HTML or regex as fallback (Req 7.2)
	properties.Property("JSON primary suggests HTML or regex as fallback", prop.ForAll(
		func(dummy int) bool {
			fallbacks := SuggestFallbacks(ParserTypeJSON)
			// First fallback should be HTML (next most reliable)
			if len(fallbacks) == 0 {
				return false
			}
			return fallbacks[0].ParserType == ParserTypeHTML
		},
		gen.IntRange(1, 100),
	))

	// Property: HTML primary suggests regex or LLM as fallback (Req 7.3)
	properties.Property("HTML primary suggests JSON or regex as fallback", prop.ForAll(
		func(dummy int) bool {
			fallbacks := SuggestFallbacks(ParserTypeHTML)
			// First fallback should be JSON (most reliable)
			if len(fallbacks) == 0 {
				return false
			}
			return fallbacks[0].ParserType == ParserTypeJSON
		},
		gen.IntRange(1, 100),
	))

	// Property: Regex primary suggests JSON or HTML as fallback
	properties.Property("Regex primary suggests JSON or HTML as fallback", prop.ForAll(
		func(dummy int) bool {
			fallbacks := SuggestFallbacks(ParserTypeRegex)
			// First fallback should be JSON (most reliable)
			if len(fallbacks) == 0 {
				return false
			}
			return fallbacks[0].ParserType == ParserTypeJSON
		},
		gen.IntRange(1, 100),
	))

	// Property: LLM primary suggests JSON, HTML, or regex as fallback
	properties.Property("LLM primary suggests JSON, HTML, or regex as fallback", prop.ForAll(
		func(dummy int) bool {
			fallbacks := SuggestFallbacks(ParserTypeLLM)
			// First fallback should be JSON (most reliable)
			if len(fallbacks) == 0 {
				return false
			}
			return fallbacks[0].ParserType == ParserTypeJSON
		},
		gen.IntRange(1, 100),
	))

	// Property: GetBestFallback returns a fallback different from primary
	properties.Property("GetBestFallback returns a fallback different from primary", prop.ForAll(
		func(primaryParser string) bool {
			bestFallback := GetBestFallback(primaryParser)
			if bestFallback == nil {
				return false
			}
			return bestFallback.ParserType != primaryParser
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: EnhanceSchemaWithFallback adds fallback to schema without one
	properties.Property("EnhanceSchemaWithFallback adds fallback to schema without one", prop.ForAll(
		func(primaryParser string) bool {
			schema := &PackageConfig{
				URL:    "https://example.com/api",
				Parser: primaryParser,
			}

			EnhanceSchemaWithFallback(schema)

			// Schema should now have a fallback
			return schema.FallbackParser != "" && schema.FallbackParser != primaryParser
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: EnhanceSchemaWithFallback does not override existing fallback
	properties.Property("EnhanceSchemaWithFallback does not override existing fallback", prop.ForAll(
		func(primaryParser, existingFallback string) bool {
			schema := &PackageConfig{
				URL:            "https://example.com/api",
				Parser:         primaryParser,
				FallbackParser: existingFallback,
			}

			EnhanceSchemaWithFallback(schema)

			// Existing fallback should be preserved
			return schema.FallbackParser == existingFallback
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex),
		gen.OneConstOf(ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: Fallback suggestions have valid parser types
	properties.Property("Fallback suggestions have valid parser types", prop.ForAll(
		func(primaryParser string) bool {
			fallbacks := SuggestFallbacks(primaryParser)
			validTypes := map[string]bool{
				ParserTypeJSON:  true,
				ParserTypeHTML:  true,
				ParserTypeRegex: true,
				ParserTypeLLM:   true,
			}
			for _, fb := range fallbacks {
				if !validTypes[fb.ParserType] {
					return false
				}
			}
			return true
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: Each fallback has a non-empty reason
	properties.Property("Each fallback has a non-empty reason", prop.ForAll(
		func(primaryParser string) bool {
			fallbacks := SuggestFallbacks(primaryParser)
			for _, fb := range fallbacks {
				if fb.Reason == "" {
					return false
				}
			}
			return true
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	properties.TestingRun(t)
}

// TestFallbackOrdering tests Property 18: Fallback Ordering
// **Feature: autoupdate-analyzer, Property 18: Fallback Ordering**
// **Validates: Requirements 7.5**
//
// For any schema with multiple fallback options, they SHALL be ordered by
// reliability: JSON > HTML > regex > LLM.
func TestFallbackOrdering(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: SuggestFallbacks returns fallbacks in reliability order
	properties.Property("SuggestFallbacks returns fallbacks in reliability order", prop.ForAll(
		func(primaryParser string) bool {
			fallbacks := SuggestFallbacks(primaryParser)
			return IsFallbackOrderValid(fallbacks)
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: JSON has highest reliability (lowest score)
	properties.Property("JSON has highest reliability", prop.ForAll(
		func(dummy int) bool {
			jsonReliability := GetParserReliability(ParserTypeJSON)
			htmlReliability := GetParserReliability(ParserTypeHTML)
			regexReliability := GetParserReliability(ParserTypeRegex)
			llmReliability := GetParserReliability(ParserTypeLLM)

			return jsonReliability < htmlReliability &&
				jsonReliability < regexReliability &&
				jsonReliability < llmReliability
		},
		gen.IntRange(1, 100),
	))

	// Property: HTML has second highest reliability
	properties.Property("HTML has second highest reliability", prop.ForAll(
		func(dummy int) bool {
			jsonReliability := GetParserReliability(ParserTypeJSON)
			htmlReliability := GetParserReliability(ParserTypeHTML)
			regexReliability := GetParserReliability(ParserTypeRegex)
			llmReliability := GetParserReliability(ParserTypeLLM)

			return htmlReliability > jsonReliability &&
				htmlReliability < regexReliability &&
				htmlReliability < llmReliability
		},
		gen.IntRange(1, 100),
	))

	// Property: Regex has third highest reliability
	properties.Property("Regex has third highest reliability", prop.ForAll(
		func(dummy int) bool {
			htmlReliability := GetParserReliability(ParserTypeHTML)
			regexReliability := GetParserReliability(ParserTypeRegex)
			llmReliability := GetParserReliability(ParserTypeLLM)

			return regexReliability > htmlReliability &&
				regexReliability < llmReliability
		},
		gen.IntRange(1, 100),
	))

	// Property: LLM has lowest reliability
	properties.Property("LLM has lowest reliability", prop.ForAll(
		func(dummy int) bool {
			jsonReliability := GetParserReliability(ParserTypeJSON)
			htmlReliability := GetParserReliability(ParserTypeHTML)
			regexReliability := GetParserReliability(ParserTypeRegex)
			llmReliability := GetParserReliability(ParserTypeLLM)

			return llmReliability > jsonReliability &&
				llmReliability > htmlReliability &&
				llmReliability > regexReliability
		},
		gen.IntRange(1, 100),
	))

	// Property: OrderFallbacksByReliability produces valid order
	properties.Property("OrderFallbacksByReliability produces valid order", prop.ForAll(
		func(primaryParser string) bool {
			// Get fallbacks (may be in any order)
			fallbacks := SuggestFallbacks(primaryParser)

			// Shuffle them (simulate unordered input)
			shuffled := make([]FallbackSuggestion, len(fallbacks))
			copy(shuffled, fallbacks)
			// Reverse to simulate disorder
			for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
				shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
			}

			// Order them
			ordered := OrderFallbacksByReliability(shuffled)

			// Check order is valid
			return IsFallbackOrderValid(ordered)
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: First fallback for non-JSON primary is JSON
	properties.Property("First fallback for non-JSON primary is JSON", prop.ForAll(
		func(primaryParser string) bool {
			if primaryParser == ParserTypeJSON {
				return true // Skip JSON primary
			}
			fallbacks := SuggestFallbacks(primaryParser)
			if len(fallbacks) == 0 {
				return false
			}
			return fallbacks[0].ParserType == ParserTypeJSON
		},
		gen.OneConstOf(ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: First fallback for JSON primary is HTML
	properties.Property("First fallback for JSON primary is HTML", prop.ForAll(
		func(dummy int) bool {
			fallbacks := SuggestFallbacks(ParserTypeJSON)
			if len(fallbacks) == 0 {
				return false
			}
			return fallbacks[0].ParserType == ParserTypeHTML
		},
		gen.IntRange(1, 100),
	))

	// Property: Reliability scores are consistent
	properties.Property("Reliability scores are consistent", prop.ForAll(
		func(parserType string) bool {
			// Get reliability twice, should be same
			r1 := GetParserReliability(parserType)
			r2 := GetParserReliability(parserType)
			return r1 == r2
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	// Property: Unknown parser types get lowest reliability
	properties.Property("Unknown parser types get lowest reliability", prop.ForAll(
		func(unknownType string) bool {
			unknownReliability := GetParserReliability(unknownType)
			llmReliability := GetParserReliability(ParserTypeLLM)
			return unknownReliability > llmReliability
		},
		gen.OneConstOf("unknown", "invalid", "custom", "xml"),
	))

	// Property: IsFallbackOrderValid returns true for empty list
	properties.Property("IsFallbackOrderValid returns true for empty list", prop.ForAll(
		func(dummy int) bool {
			return IsFallbackOrderValid([]FallbackSuggestion{})
		},
		gen.IntRange(1, 100),
	))

	// Property: IsFallbackOrderValid returns true for single item
	properties.Property("IsFallbackOrderValid returns true for single item", prop.ForAll(
		func(parserType string) bool {
			single := []FallbackSuggestion{
				{ParserType: parserType, Reliability: GetParserReliability(parserType)},
			}
			return IsFallbackOrderValid(single)
		},
		gen.OneConstOf(ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests
// =============================================================================

// TestGetParserReliability tests the reliability scoring function
func TestGetParserReliability(t *testing.T) {
	tests := []struct {
		parserType string
		expected   ParserReliability
	}{
		{ParserTypeJSON, ReliabilityJSON},
		{ParserTypeHTML, ReliabilityHTML},
		{ParserTypeRegex, ReliabilityRegex},
		{ParserTypeLLM, ReliabilityLLM},
		{"unknown", ReliabilityLLM + 1},
	}

	for _, tc := range tests {
		result := GetParserReliability(tc.parserType)
		if result != tc.expected {
			t.Errorf("GetParserReliability(%q) = %d, expected %d", tc.parserType, result, tc.expected)
		}
	}
}

// TestSuggestFallbacksCount tests that correct number of fallbacks are suggested
func TestSuggestFallbacksCount(t *testing.T) {
	tests := []struct {
		primaryParser string
		expectedCount int
	}{
		{ParserTypeJSON, 3},  // HTML, regex, LLM
		{ParserTypeHTML, 3},  // JSON, regex, LLM
		{ParserTypeRegex, 3}, // JSON, HTML, LLM
		{ParserTypeLLM, 3},   // JSON, HTML, regex
	}

	for _, tc := range tests {
		fallbacks := SuggestFallbacks(tc.primaryParser)
		if len(fallbacks) != tc.expectedCount {
			t.Errorf("SuggestFallbacks(%q) returned %d fallbacks, expected %d",
				tc.primaryParser, len(fallbacks), tc.expectedCount)
		}
	}
}

// TestApplyFallbackToSchema tests applying fallback to schema
func TestApplyFallbackToSchema(t *testing.T) {
	tests := []struct {
		name            string
		fallbackType    string
		expectPattern   bool
		expectLLMPrompt bool
	}{
		{"regex fallback", ParserTypeRegex, true, false},
		{"llm fallback", ParserTypeLLM, false, true},
		{"json fallback", ParserTypeJSON, false, false},
		{"html fallback", ParserTypeHTML, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema := &PackageConfig{
				URL:    "https://example.com",
				Parser: ParserTypeJSON,
			}

			fallback := &FallbackSuggestion{
				ParserType:  tc.fallbackType,
				Reliability: GetParserReliability(tc.fallbackType),
			}

			ApplyFallbackToSchema(schema, fallback)

			if schema.FallbackParser != tc.fallbackType {
				t.Errorf("Expected FallbackParser %q, got %q", tc.fallbackType, schema.FallbackParser)
			}

			if tc.expectPattern && schema.FallbackPattern == "" {
				t.Error("Expected FallbackPattern to be set for regex fallback")
			}

			if tc.expectLLMPrompt && schema.LLMPrompt == "" {
				t.Error("Expected LLMPrompt to be set for LLM fallback")
			}
		})
	}
}

// TestApplyFallbackToSchemaNil tests nil handling
func TestApplyFallbackToSchemaNil(t *testing.T) {
	// Should not panic
	ApplyFallbackToSchema(nil, nil)
	ApplyFallbackToSchema(&PackageConfig{}, nil)
	ApplyFallbackToSchema(nil, &FallbackSuggestion{})
}

// TestValidateFallbackChain tests fallback chain validation
func TestValidateFallbackChain(t *testing.T) {
	tests := []struct {
		name        string
		schema      *PackageConfig
		expectError bool
	}{
		{
			name:        "nil schema",
			schema:      nil,
			expectError: false,
		},
		{
			name: "no fallback",
			schema: &PackageConfig{
				Parser: ParserTypeJSON,
			},
			expectError: false,
		},
		{
			name: "valid fallback",
			schema: &PackageConfig{
				Parser:          ParserTypeJSON,
				FallbackParser:  ParserTypeRegex,
				FallbackPattern: `(\d+\.\d+)`,
			},
			expectError: false,
		},
		{
			name: "same as primary",
			schema: &PackageConfig{
				Parser:         ParserTypeJSON,
				FallbackParser: ParserTypeJSON,
			},
			expectError: true,
		},
		{
			name: "invalid fallback type",
			schema: &PackageConfig{
				Parser:         ParserTypeJSON,
				FallbackParser: "invalid",
			},
			expectError: true,
		},
		{
			name: "regex fallback without pattern",
			schema: &PackageConfig{
				Parser:         ParserTypeJSON,
				FallbackParser: ParserTypeRegex,
			},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFallbackChain(tc.schema)
			if tc.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tc.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}
