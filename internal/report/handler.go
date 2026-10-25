package report

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

type Handler struct {
	sources *Sources
}

func NewHandler(sources *Sources) *Handler {
	return &Handler{sources: sources}
}

// RegisterHandlers は以下を登録する。
//
//	GET /           グループ一覧 (JSON, 新しい順)
//	GET /:group     レポート。:group は "latest" も可
//	                ?top=N           各表の行数 (既定 15)
//	                ?compare=prev    直前のグループとの差分を付ける (グループID指定も可)
//	                ?format=json     Markdown ではなく JSON で返す
func (h *Handler) RegisterHandlers(g *echo.Group) {
	g.GET("", h.getIndex)
	g.GET("/:group", h.getReport)
}

func (h *Handler) getIndex(c echo.Context) error {
	return c.JSON(http.StatusOK, h.sources.Groups())
}

func (h *Handler) getReport(c echo.Context) error {
	opts := Options{Top: DefaultTop, Compare: c.QueryParam("compare")}
	if v := c.QueryParam("top"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "top must be a positive integer")
		}
		opts.Top = n
	}

	rep, err := h.sources.Build(c.Param("group"), opts)
	if errors.Is(err, ErrGroupNotFound) {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	} else if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("failed to build report: %v", err))
	}

	switch c.QueryParam("format") {
	case "", "markdown", "md":
		buf := &bytes.Buffer{}
		if err := rep.WriteMarkdown(buf); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("failed to render report: %v", err))
		}
		return c.Blob(http.StatusOK, "text/markdown; charset=utf-8", buf.Bytes())
	case "json":
		return c.JSON(http.StatusOK, rep)
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "format must be markdown or json")
	}
}
