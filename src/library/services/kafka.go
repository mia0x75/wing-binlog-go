package services

import (
	kafka "github.com/segmentio/kafka-go"
	"context"
)

type WKafka struct {
	writer *kafka.Writer
}

func NewKafkaService() *WKafka {
	w := kafka.NewWriter(kafka.WriterConfig{
		Brokers:  []string{"localhost:9092"},
		Topic:    "topic-A",
		Balancer: &kafka.LeastBytes{},
	})
	return &WKafka{writer:w}
}

func (wk *WKafka) SendAll() {
	wk.writer.WriteMessages(context.Background(),
		kafka.Message{
			Key:   []byte("Key-A"),
			Value: []byte("Hello World!"),
		},
		kafka.Message{
			Key:   []byte("Key-B"),
			Value: []byte("One!"),
		},
		kafka.Message{
			Key:   []byte("Key-C"),
			Value: []byte("Two!"),
		},
	)
	wk.writer.Close()
}