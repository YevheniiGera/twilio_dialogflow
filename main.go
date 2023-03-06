package main

import (
	"context"
	"fmt"
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
	"github.com/twilio/twilio-go"
	"github.com/twilio/twilio-go/twiml"
	"google.golang.org/api/option"
)

var TwilioAccountSid = ""
var TwilioToken = ""

var DialogflowProjectId = ""
var DialogflowCredentials = option.WithCredentialsFile("")

var twilioClient = twilio.NewRestClientWithParams(twilio.ClientParams{
	Username: TwilioAccountSid,
	Password: TwilioToken,
})

func main() {
	app := fiber.New()

	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			c.Locals("hostname", c.Hostname())
			c.Locals("ctx", c.Context())

			return c.Next()
		}

		return fiber.ErrUpgradeRequired
	})

	app.Post("/twiml", func(c *fiber.Ctx) error {
		stream := &twiml.VoiceStream{Url: fmt.Sprintf("wss://%s/ws/media", c.Hostname())}

		start := &twiml.VoiceConnect{
			InnerElements: []twiml.Element{stream},
		}

		return voiceResponse(c, start)
	})

	app.Get("/ws/media", websocket.New(func(c *websocket.Conn) {
		defer c.Close()

		ctx := c.Locals("ctx").(context.Context)
		host := c.Locals("hostname").(string)

		service := NewDialogflowService(twilioClient, fmt.Sprintf("https://%s/redirect", host))

		defer service.close()

		if err := service.StartSession(ctx, DialogflowProjectId, DialogflowCredentials); err != nil {
			log.Fatalln("Failed to create Dialogflow Session client")
		}

		if err := service.HandleConnection(c); err != nil {
			log.Println("Failed to process websocket connection", err)
		}
	}))

	app.Post("/redirect", func(c *fiber.Ctx) error {
		return c.SendString("Hello, World 👋!")
	})

	app.Post("/fulfillment", func(c *fiber.Ctx) error {
		req := &FullfillmentReq{}
		if err := c.BodyParser(req); err != nil {
			println(err)
		}

		msg := "Ok. I cannot connect you right now"

		if len(req.QueryResult.Parameters) > 0 {
			msg = fmt.Sprintf("%s is busy. Talk to me", req.QueryResult.Parameters["name"])
		}

		resp := &FullfillmentResp{
			FulfillmentMessages: []FullfillmentMessage{
				{Text: &FullfillmentText{Text: []string{msg}}},
			},
		}

		return c.JSON(resp)
	})

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Hello, World 👋!")
	})

	app.Listen(":3000")
}

func voiceResponse(c *fiber.Ctx, verbs ...twiml.Element) error {
	c.Set("Content-type", "application/xml; charset=utf-8")

	xml, err := twiml.Voice(verbs)
	if err != nil {
		return fmt.Errorf("failed to create voice response: %w", err)
	}

	return c.SendString(xml)
}
