// Package autoupdate provides fallback chain logic for parser configuration.
package autoupdate

// ParserReliability defines the reliability order of parsers.
// Lower values indicate higher reliability.
// Order: JSON (1) > HTML (2) > regex (3) > LLM (4)
type ParserReliability int

const (
	// ReliabilityJSON is the highest reliability (structured data)
	ReliabilityJSON ParserReliability = 1
	// ReliabilityHTML is second highest (semi-structured)
	ReliabilityHTML ParserReliability = 2
	// ReliabilityRegex is third (pattern matching)
	ReliabilityRegex ParserReliability = 3
	// ReliabilityLLM is lowest (AI-based extraction)
	ReliabilityLLM ParserReliability = 4
)

// ParserType constants for parser types
const (
	ParserTypeJSON  = "json"
	ParserTypeHTML  = "html"
	ParserTypeRegex = "regex"
	ParserTypeLLM   = "llm"
)

// FallbackSuggestion represents a suggested fallback parser configuration.
type FallbackSuggestion struct {
	// ParserType is the type of fallback parser
	ParserType string
	// Reliability is the reliability score (lower is better)
	Reliability ParserReliability
	// Reason explains why this fallback is suggested
	Reason string
}

// GetParserReliability returns the reliability score for a parser type.
// Lower scores indicate higher reliability.
func GetParserReliability(parserType string) ParserReliability {
	switch parserType {
	case ParserTypeJSON:
		return ReliabilityJSON
	case ParserTypeHTML:
		return ReliabilityHTML
	case ParserTypeRegex:
		return ReliabilityRegex
	case ParserTypeLLM:
		return ReliabilityLLM
	default:
		// Unknown parsers get lowest reliability
		return ReliabilityLLM + 1
	}
}

// SuggestFallbacks suggests appropriate fallback parsers based on the primary parser type.
// It returns fallbacks ordered by reliability (JSON > HTML > regex > LLM).
// The primary parser type is excluded from suggestions.
func SuggestFallbacks(primaryParser string) []FallbackSuggestion {
	var suggestions []FallbackSuggestion

	// Define all possible fallbacks with their reasons
	allFallbacks := map[string]FallbackSuggestion{
		ParserTypeJSON: {
			ParserType:  ParserTypeJSON,
			Reliability: ReliabilityJSON,
			Reason:      "JSON provides structured, reliable version data",
		},
		ParserTypeHTML: {
			ParserType:  ParserTypeHTML,
			Reliability: ReliabilityHTML,
			Reason:      "HTML parsing with CSS selectors or XPath is semi-structured",
		},
		ParserTypeRegex: {
			ParserType:  ParserTypeRegex,
			Reliability: ReliabilityRegex,
			Reason:      "Regex pattern matching works on any text content",
		},
		ParserTypeLLM: {
			ParserType:  ParserTypeLLM,
			Reliability: ReliabilityLLM,
			Reason:      "LLM extraction handles complex or unstructured content",
		},
	}

	// Add fallbacks in reliability order, excluding the primary parser
	orderedTypes := []string{ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM}
	for _, parserType := range orderedTypes {
		if parserType != primaryParser {
			suggestions = append(suggestions, allFallbacks[parserType])
		}
	}

	return suggestions
}

// GetBestFallback returns the single best fallback for a given primary parser.
// It returns the most reliable fallback that is different from the primary parser.
func GetBestFallback(primaryParser string) *FallbackSuggestion {
	suggestions := SuggestFallbacks(primaryParser)
	if len(suggestions) == 0 {
		return nil
	}
	return &suggestions[0]
}

// OrderFallbacksByReliability sorts fallback suggestions by reliability.
// This ensures fallbacks are tried in order of reliability (JSON > HTML > regex > LLM).
func OrderFallbacksByReliability(fallbacks []FallbackSuggestion) []FallbackSuggestion {
	// Create a copy to avoid modifying the original
	result := make([]FallbackSuggestion, len(fallbacks))
	copy(result, fallbacks)

	// Simple insertion sort (small list, stable sort)
	for i := 1; i < len(result); i++ {
		key := result[i]
		j := i - 1
		for j >= 0 && result[j].Reliability > key.Reliability {
			result[j+1] = result[j]
			j--
		}
		result[j+1] = key
	}

	return result
}

// IsFallbackOrderValid checks if fallbacks are ordered by reliability.
// Returns true if fallbacks are in correct order (JSON > HTML > regex > LLM).
func IsFallbackOrderValid(fallbacks []FallbackSuggestion) bool {
	if len(fallbacks) <= 1 {
		return true
	}

	for i := 1; i < len(fallbacks); i++ {
		if fallbacks[i].Reliability < fallbacks[i-1].Reliability {
			return false
		}
	}

	return true
}

// ApplyFallbackToSchema applies a fallback suggestion to a PackageConfig.
// It sets the FallbackParser field based on the suggestion.
func ApplyFallbackToSchema(schema *PackageConfig, fallback *FallbackSuggestion) {
	if schema == nil || fallback == nil {
		return
	}

	schema.FallbackParser = fallback.ParserType

	// Set default fallback configuration based on parser type
	switch fallback.ParserType {
	case ParserTypeRegex:
		// Default regex pattern for version extraction
		if schema.FallbackPattern == "" {
			schema.FallbackPattern = `(\d+\.\d+(?:\.\d+)?(?:[-._]\w+)?)`
		}
	case ParserTypeLLM:
		// LLM doesn't need a pattern, uses LLMPrompt if set
		if schema.LLMPrompt == "" {
			schema.LLMPrompt = "Extract the version number from the content"
		}
	}
}

// EnhanceSchemaWithFallback adds fallback configuration to a schema based on its primary parser.
// This is called after LLM analysis to ensure fallback is always configured.
func EnhanceSchemaWithFallback(schema *PackageConfig) {
	if schema == nil {
		return
	}

	// If fallback is already configured, don't override
	if schema.FallbackParser != "" {
		return
	}

	// Get the best fallback for the primary parser
	bestFallback := GetBestFallback(schema.Parser)
	if bestFallback != nil {
		ApplyFallbackToSchema(schema, bestFallback)
	}
}

// ValidateFallbackChain validates that a schema's fallback configuration is valid.
// It checks that the fallback parser is different from the primary parser
// and that required fields are set.
func ValidateFallbackChain(schema *PackageConfig) error {
	if schema == nil {
		return nil
	}

	// No fallback configured is valid
	if schema.FallbackParser == "" {
		return nil
	}

	// Fallback must be different from primary
	if schema.FallbackParser == schema.Parser {
		return ErrInvalidParserType
	}

	// Validate fallback parser type
	switch schema.FallbackParser {
	case ParserTypeJSON, ParserTypeHTML, ParserTypeRegex, ParserTypeLLM:
		// Valid parser types
	default:
		return ErrInvalidParserType
	}

	// Validate required fields for fallback parser
	if schema.FallbackParser == ParserTypeRegex {
		if schema.FallbackPattern == "" {
			return ErrMissingPattern
		}
	}

	return nil
}
