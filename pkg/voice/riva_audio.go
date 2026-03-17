package voice

import (
	"encoding/binary"
	"fmt"
	"os"
)

type rivaAudioPayload struct {
	bytes        []byte
	sampleRate   int32
	channelCount int32
}

func readRivaAudioPayload(audioFilePath string) (*rivaAudioPayload, error) {
	data, err := os.ReadFile(audioFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file: %w", err)
	}

	payload := &rivaAudioPayload{bytes: data}
	if sampleRate, channels, ok := detectWAVSpecs(data); ok {
		payload.sampleRate = sampleRate
		payload.channelCount = channels
	}

	return payload, nil
}

func detectWAVSpecs(data []byte) (sampleRate int32, channelCount int32, ok bool) {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, 0, false
	}

	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		offset += 8
		if offset+chunkSize > len(data) {
			return 0, 0, false
		}

		if chunkID == "fmt " && chunkSize >= 16 {
			audioFormat := binary.LittleEndian.Uint16(data[offset : offset+2])
			if audioFormat != 1 {
				return 0, 0, false
			}
			channelCount = int32(binary.LittleEndian.Uint16(data[offset+2 : offset+4]))
			sampleRate = int32(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
			if sampleRate > 0 && channelCount > 0 {
				return sampleRate, channelCount, true
			}
			return 0, 0, false
		}

		offset += chunkSize
		if chunkSize%2 == 1 {
			offset++
		}
	}

	return 0, 0, false
}
