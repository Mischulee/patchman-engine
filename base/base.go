package base

import (
	"app/base/utils"
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

const VMaaSAPIPrefix = "/api/v3"
const RBACApiPrefix = "/api/rbac/v1"

// Go datetime parser does not like slightly incorrect RFC 3339 which we are using (missing Z )
const Rfc3339NoTz = "2006-01-02T15:04:05-07:00"

var Context context.Context
var CancelContext context.CancelFunc

func init() {
	Context, CancelContext = context.WithCancel(context.Background())
}

func HandleSignals() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-c
		CancelContext()
		utils.Log().Info("SIGTERM/SIGINT handled")
	}()
}

func remove(r rune) rune {
	if r == 0 {
		return -1
	}
	return r
}

// Removes characters, which are not accepted by postgresql driver
// in parameter values
func RemoveInvalidChars(s string) string {
	return strings.Map(remove, s)
}

type Rfc3339Timestamp time.Time
type Rfc3339TimestampWithZ time.Time

func unmarshalTimestamp(data []byte, format string) (time.Time, error) {
	var jd string
	var err error
	var t time.Time
	if err = json.Unmarshal(data, &jd); err != nil {
		return t, err
	}
	t, err = time.Parse(format, jd)
	return t, err
}

func (d Rfc3339Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Time().Format(Rfc3339NoTz))
}

func (d *Rfc3339Timestamp) UnmarshalJSON(data []byte) error {
	t, err := unmarshalTimestamp(data, Rfc3339NoTz)
	*d = Rfc3339Timestamp(t)
	return err
}

func (d *Rfc3339Timestamp) Time() *time.Time {
	if d == nil {
		return nil
	}
	return (*time.Time)(d)
}

func (d Rfc3339TimestampWithZ) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Time().Format(time.RFC3339))
}

func (d *Rfc3339TimestampWithZ) UnmarshalJSON(data []byte) error {
	t, err := unmarshalTimestamp(data, time.RFC3339)
	*d = Rfc3339TimestampWithZ(t)
	return err
}

func (d *Rfc3339TimestampWithZ) Time() *time.Time {
	if d == nil {
		return nil
	}
	return (*time.Time)(d)
}

// TryExposeOnMetricsPort Expose app on required port if set
func TryExposeOnMetricsPort(app *gin.Engine) {
	metricsPort := utils.Cfg.MetricsPort
	if metricsPort == -1 {
		return // Do not expose extra metrics port if not set
	}
	err := utils.RunServer(Context, app, metricsPort)
	if err != nil {
		utils.Log("err", err.Error()).Error()
		panic(err)
	}
}
