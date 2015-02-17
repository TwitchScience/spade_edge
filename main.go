package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/afex/hystrix-go/hystrix"
	"github.com/twitchscience/aws_utils/environment"
	"github.com/twitchscience/aws_utils/notifier"
	"github.com/twitchscience/aws_utils/uploader"

	"github.com/twitchscience/gologging/gologging"
	gen "github.com/twitchscience/gologging/key_name_generator"
	"github.com/twitchscience/spade_edge/kafka_logger"
	"github.com/twitchscience/spade_edge/request_handler"

	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cactus/go-statsd-client/statsd"
	"github.com/crowdmob/goamz/aws"
	"github.com/crowdmob/goamz/s3"
)

const (
	auditBucketName = "spade-audits"
	eventBucketName = "spade-edge"
)

var (
	CLOUD_ENV    = environment.GetCloudEnv()
	stats_prefix = flag.String("stat_prefix", "edge", "statsd prefix")
	logging_dir  = flag.String("log_dir", ".", "The directory to log these files to")
	listen_port  = flag.String(
		"port",
		":8888",
		"Which port are we listenting on form: ':<port>' e.g. :8888",
	)
	brokers = flag.String("kafka_brokers", "",
		`<host>:<port>,<host>:<port>,... of kafka brokers.
		Can leave empty if not using`)
	clientId   = flag.String("client_id", "", "id of this client.")
	VERSION, _ = strconv.Atoi(os.Getenv("EDGE_VERSION"))
)

func ParseBrokerList(csv string) []string {
	return strings.Split(csv, ",")
}

type DummyNotifierHarness struct {
}
type SQSNotifierHarness struct {
	qName    string
	version  int
	notifier *notifier.SQSClient
}
type SQSErrorHarness struct {
	qName    string
	notifier *notifier.SQSClient
}

func (d *DummyNotifierHarness) SendMessage(r *uploader.UploadReceipt) error {
	return nil
}

func BuildSQSNotifierHarness() *SQSNotifierHarness {
	client := notifier.DefaultClient
	client.Signer.RegisterMessageType("edge", func(args ...interface{}) (string, error) {
		if len(args) < 2 {
			return "", errors.New("Missing correct number of args ")
		}
		return fmt.Sprintf("{\"version\":%d,\"keyname\":%q}", args...), nil
	})
	return &SQSNotifierHarness{
		qName:    fmt.Sprintf("spade-edge-%s", CLOUD_ENV),
		version:  VERSION,
		notifier: client,
	}
}

func BuildSQSErrorHarness() *SQSErrorHarness {
	return &SQSErrorHarness{
		qName:    fmt.Sprintf("uploader-error-spade-edge-%s", CLOUD_ENV),
		notifier: notifier.DefaultClient,
	}
}

func (s *SQSErrorHarness) SendError(er error) {
	err := s.notifier.SendMessage("error", s.qName, er)
	if err != nil {
		log.Println(err)
	}
}

func (s *SQSNotifierHarness) SendMessage(message *uploader.UploadReceipt) error {
	return s.notifier.SendMessage("edge", s.qName, s.version, message.KeyName)
}

func initStatsd(statsPrefix, statsdHostport string) (stats statsd.Statter, err error) {
	if statsdHostport == "" {
		stats, _ = statsd.NewNoop()
	} else {
		if stats, err = statsd.New(statsdHostport, *stats_prefix); err != nil {
			log.Fatalf("Statsd configuration error: %v", err)
		}
	}
	return
}

const MAX_LINES_PER_LOG = 1000000 // 1 million

func main() {
	flag.Parse()

	stats, err := initStatsd(*stats_prefix, os.Getenv("STATSD_HOSTPORT"))
	if err != nil {
		log.Fatalf("Statsd configuration error: %v", err)
	}

	auth, err := aws.GetAuth("", "", "", time.Now())
	if err != nil {
		log.Fatalln("Failed to recieve auth from env")
	}
	awsConnection := s3.New(
		auth,
		aws.USWest2,
	)

	auditBucket := awsConnection.Bucket(auditBucketName + "-" + CLOUD_ENV)
	auditBucket.PutBucket(s3.BucketOwnerFull)
	eventBucket := awsConnection.Bucket(eventBucketName + "-" + CLOUD_ENV)
	eventBucket.PutBucket(s3.BucketOwnerFull)

	auditInfo := gen.BuildInstanceInfo(&gen.EnvInstanceFetcher{}, "spade_edge_audit", *logging_dir)
	loggingInfo := gen.BuildInstanceInfo(&gen.EnvInstanceFetcher{}, "spade_edge", *logging_dir)

	auditRotateCoordinator := gologging.NewRotateCoordinator(MAX_LINES_PER_LOG, time.Minute*10)
	loggingRotateCoordinator := gologging.NewRotateCoordinator(MAX_LINES_PER_LOG, time.Minute*10)

	auditLogger, err := gologging.StartS3Logger(
		auditRotateCoordinator,
		auditInfo,
		&DummyNotifierHarness{},
		&uploader.S3UploaderBuilder{
			Bucket:           auditBucket,
			KeyNameGenerator: &gen.EdgeKeyNameGenerator{Info: auditInfo},
		},
		BuildSQSErrorHarness(),
		2,
	)
	if err != nil {
		log.Fatalf("Got Error while building audit: %s\n", err)
	}

	spadeEventLogger, err := gologging.StartS3Logger(
		loggingRotateCoordinator,
		loggingInfo,
		BuildSQSNotifierHarness(),
		&uploader.S3UploaderBuilder{
			Bucket:           eventBucket,
			KeyNameGenerator: &gen.EdgeKeyNameGenerator{Info: loggingInfo},
		},
		BuildSQSErrorHarness(),
		2,
	)
	if err != nil {
		log.Fatalf("Got Error while building logger: %s\n", err)
	}

	// Initialize Loggers.
	// AuditLogger writes to the audit log, for analysis of system success rate.
	// SpadeLogger writes requests to a file for processing by the spade processor.
	// K(afka)Logger writes produces messages for kafka, currently in dark launch.
	// We allow the klogger to be null incase we boot up with an unresponsive kafka cluster.
	var logger *request_handler.EventLoggers
	brokerList := ParseBrokerList(*brokers)
	klogger, err := kafka_logger.NewKafkaLogger(*clientId, brokerList)
	if err == nil {
		klogger.(*kafka_logger.KafkaLogger).Init()
		logger = &request_handler.EventLoggers{
			AuditLogger: auditLogger,
			SpadeLogger: spadeEventLogger,
			KLogger:     klogger,
		}
	} else {
		log.Printf("Got Error while building logger: %s + %v\nUsing Nop Logger\n", err, brokerList)
		logger = &request_handler.EventLoggers{
			AuditLogger: auditLogger,
			SpadeLogger: spadeEventLogger,
			KLogger:     &request_handler.NoopLogger{},
		}
	}

	// Trigger close on receipt of SIGINT
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc,
		syscall.SIGINT)
	go func() {
		<-sigc
		// Cause flush
		logger.Close()
		os.Exit(0)
	}()

	hystrixStreamHandler := hystrix.NewStreamHandler()
	hystrixStreamHandler.Start()
	go http.ListenAndServe(net.JoinHostPort("", "81"), hystrixStreamHandler)

	// setup server and listen
	server := &http.Server{
		Addr: *listen_port,
		Handler: &request_handler.SpadeHandler{
			StatLogger: stats,
			EdgeLogger: logger,
			Assigner:   request_handler.Assigner,
		},
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		MaxHeaderBytes: 1 << 20, // 0.5MB
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalln(err)
	}
}
