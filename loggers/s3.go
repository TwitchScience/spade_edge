package loggers

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/service/s3/s3manager/s3manageriface"
	"github.com/aws/aws-sdk-go/service/sqs/sqsiface"

	"github.com/twitchscience/aws_utils/uploader"
	"github.com/twitchscience/gologging/gologging"
	"github.com/twitchscience/gologging/key_name_generator"
	"github.com/twitchscience/scoop_protocol/spade"
)

// DummyNotifierHarness is a struct that implements the uploader.NotifierHarness
// and uploader.NotifierHarness with nop implementations.
//
// It exists because the code that uses the Harnesses doesn't accept nil for cases
// where we do not want to notify at all.
type DummyNotifierHarness struct {
}

// SendMessage - nop implementation
func (d *DummyNotifierHarness) SendMessage(r *uploader.UploadReceipt) error {
	return nil
}

// SendError - nop implementation
func (d *DummyNotifierHarness) SendError(error) {
}

// An EventToStringFunc takes a spade event and converts it
//to a string for logging into a line oriented file on s3
type EventToStringFunc func(*spade.Event) (string, error)

type s3Logger struct {
	uploadLogger      *gologging.UploadLogger
	eventToStringFunc EventToStringFunc
}

// S3LoggerConfig configures a new SpadeEdgeLogger that writes
// lines of text to AWS S3
type S3LoggerConfig struct {
	Bucket   string
	MaxLines int
	MaxAge   string
}

// NewS3Logger returns a new SpadeEdgeLogger that events to S3 after
// transforming the events into lines of text using the printFunc
func NewS3Logger(
	config S3LoggerConfig,
	loggingDir string,
	printFunc EventToStringFunc,
	sqs sqsiface.SQSAPI,
	S3Uploader s3manageriface.UploaderAPI,
) (SpadeEdgeLogger, error) {
	maxAge, err := time.ParseDuration(config.MaxAge)
	if err != nil {
		return nil, fmt.Errorf("error parsing %s as a time.Duration: %v", config.MaxAge, err)
	}

	rotateCoordinator := gologging.NewRotateCoordinator(config.MaxLines, maxAge)
	loggingInfo := key_name_generator.BuildInstanceInfo(&key_name_generator.EnvInstanceFetcher{}, config.Bucket, loggingDir)

	s3Uploader := uploader.NewFactory(
		config.Bucket,
		&key_name_generator.EdgeKeyNameGenerator{Info: loggingInfo},
		S3Uploader,
	)

	uploadLogger, err := gologging.StartS3Logger(
		rotateCoordinator,
		loggingInfo,
		&DummyNotifierHarness{},
		s3Uploader,
		&DummyNotifierHarness{},
		2,
	)

	if err != nil {
		return nil, err
	}

	s3l := &s3Logger{
		uploadLogger:      uploadLogger,
		eventToStringFunc: printFunc,
	}

	return s3l, nil
}

func (s3l *s3Logger) Log(e *spade.Event) error {
	s, err := s3l.eventToStringFunc(e)
	if err != nil {
		return err
	}
	s3l.uploadLogger.Log(s)
	return nil
}

func (s3l *s3Logger) Close() {
	s3l.uploadLogger.Close()
}
