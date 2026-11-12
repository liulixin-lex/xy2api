package schedquality

func ClassOf(effort string, estTokens int) RequestClass {
	if estTokens < ClassSizeBoundaryTokens {
		if effort == "high" || effort == "xhigh" || effort == "max" {
			return ClassRegular
		}
		return ClassLight
	}
	switch effort {
	case "high":
		return ClassHeavy
	case "xhigh", "max":
		return ClassXHeavy
	default:
		return ClassRegular
	}
}
func EstimateTokens(bodyBytes int) int { return int(float64(bodyBytes) / BytesPerTokenEstimate) }
func Scale(model string, c RequestClass) float64 {
	row, ok := ClassScaleTable[model]
	if !ok {
		row = ClassScaleTable["default"]
	}
	return row[c]
}

// section 6 table; tests never use Scale
func TestScale(model string, tier TestTier) float64 {
	row, ok := TestScaleTable[model]
	if !ok {
		row = TestScaleTable["default"]
	}
	return row[tier]
}
