package memo

import (
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/goccy/go-json"
	"github.com/kaz/pprotein/internal/collect"
	"github.com/labstack/echo/v4"
)

type (
	textValue struct {
		Text string
	}

	requestBody struct {
		GroupId string
		Label   string
		Text    string
	}

	handler struct {
		opts      *collect.Options
		collector *collect.Collector
	}
)

func NewHandler(opts *collect.Options) *handler {
	return &handler{opts: opts}
}

func (h *handler) Register(g *echo.Group) error {
	p := &processor{}

	var err error
	h.collector, err = collect.New(p, h.opts)
	if err != nil {
		return fmt.Errorf("failed to initialize collector: %w", err)
	}

	g.GET("", h.getIndex)
	g.POST("", h.postIndex)
	g.GET("/:id", h.getId)
	return nil
}

// Collector は Register 後に有効になる。
func (h *handler) Collector() *collect.Collector {
	return h.collector
}

func (h *handler) getIndex(c echo.Context) error {
	list := h.collector.List()
	for _, m := range list {
		r, err := h.collector.Get(m.Snapshot.ID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Errorf("failed to get entry: %w", err))
		}
		buf, err := ioutil.ReadAll(r)
		r.Close()
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Errorf("failed to read entry: %v", err))
		}

		var v textValue
		json.Unmarshal(buf, &v)

		m.Message = v.Text
	}
	return c.JSON(http.StatusOK, h.collector.List())
}

func (h *handler) postIndex(c echo.Context) error {
	req := &requestBody{}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("failed to parse request body: %v", err))
	}

	if err := h.Add(req.GroupId, req.Label, req.Text); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.NoContent(http.StatusAccepted)
}

// Add はグループに memo を追加し、画面に通知する。
func (h *handler) Add(groupID, label, text string) error {
	target := &collect.SnapshotTarget{
		GroupId: groupID,
		Label:   label,
	}

	buf, err := json.Marshal(&textValue{Text: text})
	if err != nil {
		return fmt.Errorf("failed to marshal textValue: %w", err)
	}
	snapshot, err := h.collector.Add(target, buf)
	if err != nil {
		return fmt.Errorf("failed to add snapshot: %w", err)
	}

	eventData, err := json.Marshal(&collect.Entry{
		Snapshot: snapshot,
		Status:   "ok",
		Message:  text,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal entry: %w", err)
	}
	h.opts.EventHub.Publish(eventData)
	return nil
}

func (h *handler) getId(c echo.Context) error {
	r, err := h.collector.Get(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Errorf("failed to get entry: %w", err))
	}
	defer r.Close()

	return c.Stream(http.StatusOK, "application/json", r)
}
