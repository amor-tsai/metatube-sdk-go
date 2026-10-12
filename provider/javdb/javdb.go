package javdb

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/number"
	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider = (*JavDB)(nil)
	_ provider.MovieSearcher = (*JavDB)(nil)
	_ provider.ConfigSetter  = (*JavDB)(nil)
)

const (
	Name     = "JavDB"
	Priority = 1000 - 3
)

const (
	baseURL   = "https://javdb.com/"
	movieURL  = "https://javdb.com/v/%s?locale=zh"
	searchURL = "https://javdb.com/search?q=%s&f=all&locale=zh"
)

// cookieConfigKey holds an optional logged-in session cookie, e.g.
// MT_MOVIE_PROVIDER_JAVDB__COOKIE="_jdb_session=...". Censored titles are
// public, but JavDB redirects uncensored/FC2 movie pages to /login without one.
const cookieConfigKey = "cookie"

var ErrLoginRequired = errors.New("javdb: login required, set MT_MOVIE_PROVIDER_JAVDB__COOKIE")

type JavDB struct {
	*scraper.Scraper
	cookie string
}

func New() *JavDB {
	return &JavDB{
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Japanese,
			scraper.WithHeaders(map[string]string{
				"Referer": baseURL,
			}),
			scraper.WithCookies(baseURL, []*http.Cookie{
				{Name: "over18", Value: "1"},
				{Name: "locale", Value: "zh"},
			})),
	}
}

func (db *JavDB) SetConfig(config provider.Config) error {
	if !config.Has(cookieConfigKey) {
		return nil
	}
	cookie, err := config.GetString(cookieConfigKey)
	if err != nil {
		return err
	}
	db.cookie = strings.TrimSpace(cookie)
	return nil
}

func (db *JavDB) collector() *colly.Collector {
	c := db.ClonedCollector()
	if db.cookie != "" {
		c.OnRequest(func(r *colly.Request) {
			// Append to (not replace) the jar's over18/locale cookies.
			if existing := r.Headers.Get("Cookie"); existing != "" {
				r.Headers.Set("Cookie", existing+"; "+db.cookie)
			} else {
				r.Headers.Set("Cookie", db.cookie)
			}
		})
	}
	return c
}

func isLoginPage(r *colly.Response) bool {
	return r.Request.URL.Path == "/login"
}

// NormalizeMovieID keeps the ID as is: JavDB IDs (e.g. "rYD7J") are case-sensitive.
func (db *JavDB) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

func (db *JavDB) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	return db.GetMovieInfoByURL(fmt.Sprintf(movieURL, id))
}

func (db *JavDB) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return db.NormalizeMovieID(path.Base(homepage.Path)), nil
}

func (db *JavDB) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := db.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return
	}

	info = &model.MovieInfo{
		ID:            id,
		Provider:      db.Name(),
		Homepage:      fmt.Sprintf(movieURL, id),
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := db.collector()

	var loginRequired bool
	c.OnResponse(func(r *colly.Response) {
		loginRequired = isLoginPage(r)
	})

	// Number
	c.OnXML(`//h2[contains(@class,"title")]/strong[1]`, func(e *colly.XMLElement) {
		info.Number = strings.ToUpper(strings.TrimSpace(e.Text))
	})

	// Title: prefer the original (usually Japanese) title over a translation.
	var currentTitle, originTitle string
	c.OnXML(`//h2[contains(@class,"title")]/strong[contains(@class,"current-title")]`, func(e *colly.XMLElement) {
		currentTitle = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//h2[contains(@class,"title")]/span[contains(@class,"origin-title")]`, func(e *colly.XMLElement) {
		originTitle = strings.TrimSpace(e.Text)
	})

	// Cover
	c.OnXML(`//img[contains(@class,"video-cover")]`, func(e *colly.XMLElement) {
		info.CoverURL = e.Request.AbsoluteURL(e.Attr("src"))
		info.ThumbURL = info.CoverURL
	})

	// Fields
	c.OnXML(`//nav[contains(@class,"movie-panel-info")]/div[contains(@class,"panel-block")]`, func(e *colly.XMLElement) {
		value := strings.TrimSpace(e.ChildText(`.//span[@class="value"]`))
		switch strings.TrimRight(strings.TrimSpace(e.ChildText(`./strong`)), ":：") {
		case "日期":
			info.ReleaseDate = parser.ParseDate(value)
		case "時長":
			// e.g. "93 分鍾"
			info.Runtime = parser.ParseInt(regexp.MustCompile(`\d+`).FindString(value))
		case "導演":
			info.Director = e.ChildText(`.//span[@class="value"]/a`)
		case "片商":
			info.Maker = e.ChildText(`.//span[@class="value"]/a`)
		case "發行":
			info.Label = e.ChildText(`.//span[@class="value"]/a`)
		case "系列":
			info.Series = e.ChildText(`.//span[@class="value"]/a`)
		case "評分":
			// e.g. "4.29分, 由574人評價"
			if m := regexp.MustCompile(`[\d.]+`).FindString(value); m != "" {
				info.Score = parser.ParseScore(m)
			}
		case "類別":
			for _, genre := range e.ChildTexts(`.//span[@class="value"]/a`) {
				if genre = strings.TrimSpace(genre); genre != "" {
					info.Genres = append(info.Genres, genre)
				}
			}
		case "演員":
			// Male actors have no class; keep actresses only, like other providers.
			for _, actor := range e.ChildTexts(`.//span[@class="value"]/a[contains(@class,"actor-female")]`) {
				if actor = strings.TrimSpace(actor); actor != "" {
					info.Actors = append(info.Actors, actor)
				}
			}
		}
	})

	// Previews
	c.OnXML(`//div[contains(@class,"preview-images")]/a[contains(@class,"tile-item")]`, func(e *colly.XMLElement) {
		if href := e.Attr("href"); strings.HasSuffix(href, ".jpg") {
			info.PreviewImages = append(info.PreviewImages, e.Request.AbsoluteURL(href))
		}
	})

	// Preview video (only rendered for logged-in sessions)
	c.OnXML(`//video[@id="preview-video"]/source`, func(e *colly.XMLElement) {
		if src := e.Attr("src"); src != "" {
			info.PreviewVideoURL = e.Request.AbsoluteURL(src)
		}
	})

	c.OnScraped(func(_ *colly.Response) {
		info.Title = originTitle
		if info.Title == "" {
			info.Title = currentTitle
		}
	})

	if err = c.Visit(info.Homepage); err != nil {
		return nil, err
	}
	if loginRequired {
		return nil, ErrLoginRequired
	}
	return
}

func (db *JavDB) NormalizeMovieKeyword(keyword string) string {
	if db.cookie == "" && number.IsSpecial(keyword) {
		return "" // Uncensored/FC2 pages require login, so don't offer unfetchable results.
	}
	return strings.ToUpper(keyword)
}

func (db *JavDB) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	c := db.collector()

	c.OnXML(`//div[contains(@class,"movie-list")]/div[@class="item"]/a[contains(@class,"box")]`, func(e *colly.XMLElement) {
		homepage := e.Request.AbsoluteURL(e.Attr("href"))
		id, _ := db.ParseMovieIDFromURL(homepage)
		number := strings.TrimSpace(e.ChildText(`.//div[@class="video-title"]/strong`))
		// The video-title div is "<strong>NUMBER</strong> title".
		title := strings.TrimSpace(strings.TrimPrefix(
			strings.TrimSpace(e.ChildText(`.//div[@class="video-title"]`)), number))
		if title == "" {
			title = e.Attr("title")
		}
		cover := e.Request.AbsoluteURL(e.ChildAttr(`.//div[contains(@class,"cover")]/img`, "src"))
		results = append(results, &model.MovieSearchResult{
			ID:          id,
			Number:      strings.ToUpper(number),
			Title:       title,
			Provider:    db.Name(),
			Homepage:    fmt.Sprintf(movieURL, id),
			ThumbURL:    cover,
			CoverURL:    cover,
			ReleaseDate: parser.ParseDate(e.ChildText(`.//div[@class="meta"]`)),
		})
	})

	err = c.Visit(fmt.Sprintf(searchURL, url.QueryEscape(keyword)))
	return
}

func init() {
	provider.Register(Name, New)
}
