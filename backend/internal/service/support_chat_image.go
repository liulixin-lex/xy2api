package service

import (
	"bytes"
	"image/jpeg"
	"image/png"
)

type ValidatedSupportChatImage struct {
	Data        []byte
	ContentType string
	Width       int
	Height      int
}

func ValidateSupportChatImage(data []byte) (ValidatedSupportChatImage, error) {
	if len(data) > SupportChatImageMaxBytes {
		return ValidatedSupportChatImage{}, ErrSupportChatImageTooLarge
	}
	if len(data) < 12 {
		return ValidatedSupportChatImage{}, ErrSupportChatImageInvalid
	}
	validated := ValidatedSupportChatImage{Data: data}
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		validated.ContentType = "image/jpeg"
		config, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return validated, ErrSupportChatImageInvalid
		}
		validated.Width, validated.Height = config.Width, config.Height
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		validated.ContentType = "image/png"
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return validated, ErrSupportChatImageInvalid
		}
		validated.Width, validated.Height = config.Width, config.Height
	case string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		validated.ContentType = "image/webp"
		width, height, ok := detectOpenAIWebPDimensions(data[:minInt(len(data), 256)])
		if !ok {
			return validated, ErrSupportChatImageInvalid
		}
		validated.Width, validated.Height = width, height
	default:
		return validated, ErrSupportChatImageInvalid
	}
	if validated.Width < 1 || validated.Height < 1 || validated.Width > SupportChatImageMaxSide || validated.Height > SupportChatImageMaxSide {
		return validated, ErrSupportChatImageTooLarge
	}
	if int64(validated.Width)*int64(validated.Height) > SupportChatImageMaxPixels {
		return validated, ErrSupportChatImageTooLarge
	}
	return validated, nil
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
