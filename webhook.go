package main

type FullfillmentIntent struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type FullfillmentText struct {
	Text []string `json:"text"`
}

type FullfillmentMessage struct {
	Text *FullfillmentText `json:"text"`
}

type FullfillmentQueryResult struct {
	QueryText                 string                `json:"queryText"`
	Parameters                map[string]any        `json:"parameters"`
	FulfillmentText           string                `json:"fulfillmentText"`
	Intent                    FullfillmentIntent    `json:"intent"`
	FulfillmentMessages       []FullfillmentMessage `json:"fulfillmentMessages"`
	IntentDetectionConfidence int                   `json:"intentDetectionConfidence"`
	LanguageCode              string                `json:"languageCode"`
}

type FullfillmentReq struct {
	ResponseID  string                  `json:"responseId"`
	Session     string                  `json:"session"`
	QueryResult FullfillmentQueryResult `json:"queryResult"`
}

type FullfillmentResp struct {
	FulfillmentMessages []FullfillmentMessage `json:"fulfillmentMessages"`
}
