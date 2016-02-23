package loggers

import (
	"errors"
	"log"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/sendgridlabs/go-kinesis"
	"github.com/sendgridlabs/go-kinesis/batchproducer"
	"github.com/twitchscience/scoop_protocol/spade"
)

type kinesisLogger struct {
	client    *kinesis.Kinesis
	producer  batchproducer.Producer
	channel   chan []byte
	errors    chan error
	waitGroup *sync.WaitGroup
	stats     *kinesisStats
}

func NewKinesisLogger(region, streamName string) (SpadeEdgeLogger, error) {
	auth, err := kinesis.NewAuthFromMetadata()
	if err != nil {
		auth, err = kinesis.NewAuthFromEnv()
		if err != nil {
			return nil, err
		}
	}

	stats := &kinesisStats{}
	client := kinesis.New(auth, region)
	config := batchproducer.Config{
		AddBlocksWhenBufferFull: true,
		BufferSize:              10000,
		FlushInterval:           1 * time.Second,
		BatchSize:               400,
		MaxAttemptsPerRecord:    10,
		Logger:                  log.New(os.Stderr, "", log.LstdFlags),
		StatReceiver:            stats,
		StatInterval:            1 * time.Second,
	}
	producer, err := batchproducer.New(client, streamName, config)
	if err != nil {
		return nil, err
	}
	waitGroup := &sync.WaitGroup{}

	producer.Start()

	channel := make(chan []byte)
	errors := make(chan error)

	kl := &kinesisLogger{
		client:    client,
		producer:  producer,
		channel:   channel,
		errors:    errors,
		waitGroup: waitGroup,
		stats:     stats,
	}

	kl.start()

	return kl, nil
}

func (kl *kinesisLogger) start() {
	go func() {
		kl.waitGroup.Add(1)
		defer kl.waitGroup.Done()

		r := rand.New(rand.NewSource(time.Now().UnixNano()))

		defer kl.producer.Flush(time.Second, false)
		defer close(kl.errors)

		for msg := range kl.channel {
			key := strconv.FormatUint(uint64(r.Uint32()), 16)
			err := kl.producer.Add(msg, key)
			if err != nil {
				log.Printf("Error adding msg to kinesis producer queue %v", err)
				kl.errors <- err
			}
		}
	}()
}

func (kl *kinesisLogger) Log(e *spade.Event) error {
	c, err := spade.Marshal(e)
	if err != nil {
		return err
	}

	var ok bool
	select {
	case err, ok = <-kl.errors:
		if ok {
			return err
		} else {
			return errors.New("Processing halted")
		}

	case kl.channel <- c:
	}

	return nil
}

func (kl *kinesisLogger) Close() {
	close(kl.channel)
	kl.waitGroup.Wait()
	kl.stats.log()

	kl.producer.Stop()
}