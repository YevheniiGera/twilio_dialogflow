package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	dialogflow "cloud.google.com/go/dialogflow/apiv2"
	"cloud.google.com/go/dialogflow/apiv2/dialogflowpb"
	"github.com/gofiber/websocket/v2"
	"github.com/google/uuid"
	"github.com/twilio/twilio-go"
	openapi "github.com/twilio/twilio-go/rest/api/v2010"
	"github.com/twilio/twilio-go/twiml"
	"google.golang.org/api/option"
)

const (
	mulawHeaderSize = 58
	sampleRateHertz = 8000
)

var (
	outputAudioConfig = &dialogflowpb.OutputAudioConfig{
		AudioEncoding:   dialogflowpb.OutputAudioEncoding_OUTPUT_AUDIO_ENCODING_MULAW,
		SampleRateHertz: sampleRateHertz,
	}

	inputAudioConfig = &dialogflowpb.QueryInput_AudioConfig{
		AudioConfig: &dialogflowpb.InputAudioConfig{
			SingleUtterance: true,
			AudioEncoding:   dialogflowpb.AudioEncoding_AUDIO_ENCODING_MULAW,
			SampleRateHertz: sampleRateHertz,
			LanguageCode:    "en-US",
		},
	}
)

type Start struct {
	AccountSid  string      `json:"accountSid"`
	StreamSid   string      `json:"streamSid"`
	CallSid     string      `json:"callSid"`
	Tracks      []string    `json:"tracks"`
	MediaFormat MediaFormat `json:"mediaFormat"`
}

type MediaFormat struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

type Mark struct {
	Name string `json:"name"`
}

type Media struct {
	Track     string `json:"track"`
	Chunk     string `json:"chunk"`
	Timestamp string `json:"timestamp"`
	Payload   []byte `json:"payload"`
}

type StreamInputRequest struct {
	Event     string  `json:"event"`
	Start     *Start  `json:"start"`
	Media     *Media  `json:"media"`
	Mark      *Mark   `json:"mark"`
	StreamSid *string `json:"streamSid"`
}

type MediaPayload struct {
	Payload []byte `json:"payload"`
}

type MarkPayload struct {
	Name string `json:"name"`
}

type StreamOutputRequest struct {
	StreamSid string        `json:"streamSid"`
	Event     string        `json:"event"`
	Media     *MediaPayload `json:"media,omitempty"`
	Mark      *MarkPayload  `json:"mark,omitempty"`
}

type DialogflowService struct {
	ctx context.Context

	twilioClient *twilio.RestClient

	callSid   string
	streamSid string

	projectId     string
	sessionId     string
	sessionPath   string
	sessionClient *dialogflow.SessionsClient
	sessionStream dialogflowpb.Sessions_StreamingDetectIntentClient

	redirectURL string

	finalQueryResult *dialogflowpb.QueryResult

	isStopped          bool
	isInterrupted      bool
	isAudioInputPaused bool

	connection *websocket.Conn
}

func NewDialogflowService(twilioClient *twilio.RestClient, redirectURL string) *DialogflowService {
	return &DialogflowService{
		twilioClient: twilioClient,
		redirectURL:  redirectURL,
	}
}

func (s *DialogflowService) StartSession(ctx context.Context, projectID string, opts option.ClientOption) error {
	sessionClient, err := dialogflow.NewSessionsClient(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to initialize Dialogflow session. %w", err)
	}

	s.ctx = ctx
	s.projectId = projectID
	s.sessionId = uuid.New().String()
	s.sessionPath = fmt.Sprintf("projects/%s/agent/sessions/%s", projectID, s.sessionId)
	s.sessionClient = sessionClient

	return nil
}

func (s *DialogflowService) HandleConnection(c *websocket.Conn) error {
	s.connection = c

	var (
		err error
		req StreamInputRequest
	)
	for !s.isStopped && err == nil {
		if err := c.ReadJSON(&req); err != nil {
			break
		}

		switch req.Event {
		case "start":
			s.callSid = req.Start.CallSid
			s.streamSid = req.Start.StreamSid
			err = s.welcome()

		case "media":
			if !s.isAudioInputPaused {
				err = s.onTwilioMedia(req.Media.Payload)
			}

		case "mark":
			if req.Mark.Name == "endOfInteraction" {
				s.onFinalResult()
			}

		case "stop":
			s.close()
		}
	}

	return err
}

func (s *DialogflowService) GetDialogflowStream() (dialogflowpb.Sessions_StreamingDetectIntentClient, error) {
	if s.sessionStream == nil {
		dfStream, err := s.sessionClient.StreamingDetectIntent(s.ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize streaming detect intent client. %w", err)
		}

		// initialize audio input
		req := &dialogflowpb.StreamingDetectIntentRequest{
			Session:           s.sessionPath,
			OutputAudioConfig: outputAudioConfig,
			QueryInput:        &dialogflowpb.QueryInput{Input: inputAudioConfig},
		}

		if err := dfStream.Send(req); err != nil {
			return nil, fmt.Errorf("failed to initialize dialogflow audio stream. %w", err)
		}

		s.sessionStream = dfStream

		go s.rcvDialogflow()
	}

	return s.sessionStream, nil
}

func (s *DialogflowService) welcome() error {
	stream, err := s.sessionClient.StreamingDetectIntent(s.ctx)
	if err != nil {
		return fmt.Errorf("failed initialize Dialogflow streaming client. %w", err)
	}
	req := dialogflowpb.StreamingDetectIntentRequest{
		Session:           s.sessionPath,
		OutputAudioConfig: outputAudioConfig,
		QueryInput: &dialogflowpb.QueryInput{
			Input: &dialogflowpb.QueryInput_Event{
				Event: &dialogflowpb.EventInput{Name: "Welcome", LanguageCode: "en-US"}},
		},
	}

	if err := stream.Send(&req); err != nil {
		return fmt.Errorf("failed to send dialogflow welcome event. %w", err)
	}

	s.sessionStream = stream

	s.rcvDialogflow()

	return nil
}

func (s *DialogflowService) onDialogflowMedia(audio []byte) {
	log.Println("Sending audio to twilio...")

	s.send(&StreamOutputRequest{
		Event:     "media",
		StreamSid: s.streamSid,
		Media: &MediaPayload{
			Payload: audio,
		},
	})
}

func (s *DialogflowService) onInterrupted() {
	log.Println("Clearing twilio buffer...")

	s.send(&StreamOutputRequest{
		Event:     "clear",
		StreamSid: s.streamSid,
	})

	s.isInterrupted = true
}

func (s *DialogflowService) endOfInteraction() {
	log.Println("Mark end of interaction")

	s.send(&StreamOutputRequest{
		Event:     "mark",
		StreamSid: s.streamSid,
		Mark:      &MarkPayload{"endOfInteraction"},
	})
}

func (s *DialogflowService) onTwilioMedia(audio []byte) error {
	stream, err := s.GetDialogflowStream()
	if err != nil {
		return fmt.Errorf("failed get Dialogflow streamer. %w", err)
	}

	err = stream.Send(&dialogflowpb.StreamingDetectIntentRequest{InputAudio: audio})
	if err != nil {
		return fmt.Errorf("failed to send audio to dialogflow. %w", err)
	}

	return nil
}

func (s *DialogflowService) redirect() error {
	redirect := &twiml.VoiceRedirect{Url: s.redirectURL}

	xml, err := twiml.Voice([]twiml.Element{redirect})
	if err != nil {
		return fmt.Errorf("failed to create voice response: %w", err)
	}

	_, err = s.twilioClient.Api.UpdateCall(s.callSid, &openapi.UpdateCallParams{Twiml: &xml})
	if err != nil {
		return fmt.Errorf("failed to update call: %w", err)
	}

	s.close()

	return err
}

func (s *DialogflowService) rcvDialogflow() {
	for {
		resp, err := s.sessionStream.Recv()
		if err != nil {
			s.sessionStream = nil
			s.isInterrupted = false
			s.isAudioInputPaused = false
			break
		}

		if len(resp.OutputAudio) > 0 {
			s.onDialogflowMedia(resp.OutputAudio[mulawHeaderSize:])
		}

		if len(resp.RecognitionResult.GetTranscript()) > 0 {
			log.Println("Recognition result:", resp.RecognitionResult.GetTranscript())
			if !s.isInterrupted {
				s.onInterrupted()
			}
		}

		if resp.RecognitionResult.GetMessageType() == dialogflowpb.StreamingRecognitionResult_END_OF_SINGLE_UTTERANCE {
			log.Println("Intent recognized")
			s.isAudioInputPaused = true
		}

		if resp.QueryResult != nil && resp.QueryResult.Intent != nil {
			if resp.QueryResult.Intent.EndInteraction {
				log.Println("Final intent:", resp.QueryResult.Intent.DisplayName)
				s.finalQueryResult = resp.QueryResult
			} else {
				log.Println("Intent detected:", resp.QueryResult.Intent.DisplayName)
			}
		}
	}

	if s.finalQueryResult != nil {
		s.endOfInteraction()
	}
}

func (s *DialogflowService) onFinalResult() {
	log.Println("Redirect to:", s.redirectURL)
	if err := s.redirect(); err != nil {
		fmt.Println("Failed to redirect call:", err)
	}

	s.close()
}

func (s *DialogflowService) send(req any) error {
	j, err := json.Marshal(req)
	if err != nil {
		return err
	}

	return s.connection.WriteMessage(websocket.TextMessage, j)
}

func (s *DialogflowService) close() {
	if !s.isStopped {
		log.Println("Close dialogflow services")

		if s.sessionClient != nil {
			s.sessionClient.Close()
			s.sessionClient = nil
		}

		if s.sessionStream != nil {
			s.sessionStream.CloseSend()
			s.sessionStream = nil
		}

		s.isStopped = true
	}
}
