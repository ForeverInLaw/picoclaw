package asr

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	rivapb "github.com/sipeed/picoclaw/pkg/audio/asr/rivapb"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const defaultRivaMaxReceiveMessageLength = 64 * 1024 * 1024

type RivaTranscriber struct {
	apiKey                  string
	server                  string
	functionID              string
	languageCode            string
	customConfiguration     map[string]string
	useSSL                  bool
	requestTimeout          time.Duration
	maxReceiveMessageLength int

	connMu sync.Mutex
	conn   *grpc.ClientConn
}

func NewRivaTranscriber(apiKey string, cfg config.RivaVoiceConfig) *RivaTranscriber {
	timeout := time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	maxReceive := cfg.MaxReceiveMessageLength
	if maxReceive <= 0 {
		maxReceive = defaultRivaMaxReceiveMessageLength
	}

	languageCode := strings.TrimSpace(cfg.LanguageCode)
	if languageCode == "" {
		languageCode = "multi"
	}

	server := strings.TrimSpace(cfg.Server)
	if server == "" {
		server = "grpc.nvcf.nvidia.com:443"
	}

	return &RivaTranscriber{
		apiKey:                  strings.TrimSpace(apiKey),
		server:                  server,
		functionID:              strings.TrimSpace(cfg.FunctionID),
		languageCode:            languageCode,
		customConfiguration:     parseRivaCustomConfiguration(cfg.CustomConfiguration),
		useSSL:                  cfg.UseSSL,
		requestTimeout:          timeout,
		maxReceiveMessageLength: maxReceive,
	}
}

func (t *RivaTranscriber) Name() string {
	return "riva"
}

func (t *RivaTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	logger.InfoCF("voice", "Starting Riva transcription", map[string]any{
		"audio_file": audioFilePath,
		"server":     t.server,
	})

	payload, err := readRivaAudioPayload(audioFilePath)
	if err != nil {
		return nil, err
	}

	conn, err := t.getConn()
	if err != nil {
		return nil, err
	}

	requestCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(ctx, t.requestTimeout)
		defer cancel()
	}

	mdPairs := []string{"authorization", "Bearer " + t.apiKey}
	if t.functionID != "" {
		mdPairs = append(mdPairs, "function-id", t.functionID)
	}
	requestCtx = metadata.AppendToOutgoingContext(requestCtx, mdPairs...)

	req := &rivapb.RecognizeRequest{
		Config: &rivapb.RecognitionConfig{
			LanguageCode:               t.languageCode,
			MaxAlternatives:            1,
			EnableAutomaticPunctuation: true,
			CustomConfiguration:        copyStringMap(t.customConfiguration),
			SampleRateHertz:            payload.sampleRate,
			AudioChannelCount:          payload.channelCount,
		},
		Audio: payload.bytes,
	}

	client := rivapb.NewRivaSpeechRecognitionClient(conn)
	resp, err := client.Recognize(requestCtx, req)
	if err != nil {
		return nil, fmt.Errorf("riva recognize failed: %w", err)
	}

	result := mapRivaResponse(resp)
	logger.InfoCF("voice", "Riva transcription completed", map[string]any{
		"text_length":           len(result.Text),
		"language":              result.Language,
		"duration_seconds":      result.Duration,
		"transcription_preview": utils.Truncate(result.Text, 80),
	})

	return result, nil
}

func (t *RivaTranscriber) getConn() (*grpc.ClientConn, error) {
	t.connMu.Lock()
	defer t.connMu.Unlock()

	if t.conn != nil {
		return t.conn, nil
	}

	var transportCreds credentials.TransportCredentials
	if t.useSSL {
		transportCreds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		transportCreds = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(
		t.server,
		grpc.WithTransportCredentials(transportCreds),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(t.maxReceiveMessageLength)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Riva server: %w", err)
	}

	t.conn = conn
	return conn, nil
}

func parseRivaCustomConfiguration(raw string) map[string]string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, " ", ""))
	if raw == "" {
		return nil
	}

	cfg := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		cfg[parts[0]] = parts[1]
	}
	if len(cfg) == 0 {
		return nil
	}
	return cfg
}

func copyStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func mapRivaResponse(resp *rivapb.RecognizeResponse) *TranscriptionResponse {
	result := &TranscriptionResponse{}
	if resp == nil {
		return result
	}

	var transcripts []string
	detectedLanguages := make(map[string]struct{})
	for _, speechResult := range resp.GetResults() {
		if speechResult.GetAudioProcessed() > float32(result.Duration) {
			result.Duration = float64(speechResult.GetAudioProcessed())
		}
		alternatives := speechResult.GetAlternatives()
		if len(alternatives) == 0 {
			continue
		}
		top := alternatives[0]
		if text := strings.TrimSpace(top.GetTranscript()); text != "" {
			transcripts = append(transcripts, text)
		}
		for _, code := range top.GetLanguageCode() {
			code = strings.TrimSpace(code)
			if code != "" {
				detectedLanguages[code] = struct{}{}
			}
		}
	}

	result.Text = strings.TrimSpace(strings.Join(transcripts, "\n"))
	for code := range detectedLanguages {
		result.Language = code
		break
	}
	return result
}
