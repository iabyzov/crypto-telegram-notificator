package adapters

import (
	"context"

	"github.com/segmentio/kafka-go"
)

type KafkaPublisher struct {
	writer *kafka.Writer
}

func NewKafkaPublisher(writer *kafka.Writer) *KafkaPublisher {
	return &KafkaPublisher{
		writer: writer,
	}
}

func (p *KafkaPublisher) Publish(ctx context.Context, key, value []byte) error {
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   key,
		Value: value,
	})
}
