package httphandler

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// ServeSPA serves the built single-page frontend from dir. Existing files are
// served as-is (hashed bundles get a long cache lifetime); any other non-API
// GET request falls back to index.html so client-side routes deep-link
// correctly. Unknown /api/ paths keep returning a JSON 404.
func ServeSPA(r *gin.Engine, dir string) error {
	index := filepath.Join(dir, "index.html")
	if _, err := os.Stat(index); err != nil {
		return fmt.Errorf("index.html not found in %s: %w", dir, err)
	}
	root := http.Dir(dir)
	fileServer := http.FileServer(root)

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(http.StatusMethodNotAllowed)
			return
		}

		clean := path.Clean("/" + p)
		if clean != "/" {
			if f, err := root.Open(clean); err == nil {
				st, statErr := f.Stat()
				f.Close()
				if statErr == nil && !st.IsDir() {
					if clean != "/index.html" && !strings.HasSuffix(clean, ".html") {
						c.Header("Cache-Control", "public, max-age=31536000, immutable")
					}
					fileServer.ServeHTTP(c.Writer, c.Request)
					return
				}
			}
		}
		c.Header("Cache-Control", "no-cache")
		c.File(index)
	})
	return nil
}
