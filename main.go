package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

type BlogCrawler struct {
	browser *rod.Browser
	page    *rod.Page
	baseURL string
	timeout time.Duration
}

type CrawlResult struct {
	BaseURL    string   `json:"base_url"`
	BlogURLs   []string `json:"blog_urls"`
	TotalCount int      `json:"total_count"`
	CrawledAt  string   `json:"crawled_at"`
}

func NewBlogCrawler(baseURL string, timeout time.Duration) *BlogCrawler {
	return &BlogCrawler{
		baseURL: baseURL,
		timeout: timeout,
	}
}

func (bc *BlogCrawler) initializeBrowser() error {
	// Try to use system Chrome/Chromium if available
	launcher := launcher.New().
		Headless(true).
		Set("disable-blink-features", "AutomationControlled")

	// Try common Chrome/Chromium paths on macOS
	chromePaths := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}

	for _, path := range chromePaths {
		if _, err := os.Stat(path); err == nil {
			launcher = launcher.Bin(path)
			break
		}
	}

	browserURL, err := launcher.Launch()
	if err != nil {
		return fmt.Errorf("failed to launch browser: %w", err)
	}

	bc.browser = rod.New().ControlURL(browserURL)
	if err := bc.browser.Connect(); err != nil {
		return fmt.Errorf("failed to connect to browser: %w", err)
	}

	return nil
}

func (bc *BlogCrawler) navigateToPage() error {
	// Create a new page
	var pageErr error
	bc.page = func() *rod.Page {
		defer func() {
			if r := recover(); r != nil {
				pageErr = fmt.Errorf("failed to create page: %v", r)
			}
		}()
		return bc.browser.MustPage("")
	}()

	if pageErr != nil {
		return pageErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), bc.timeout)
	defer cancel()

	if err := bc.page.Context(ctx).Navigate(bc.baseURL); err != nil {
		return fmt.Errorf("failed to navigate to %s: %w", bc.baseURL, err)
	}

	if err := bc.page.Context(ctx).WaitLoad(); err != nil {
		return fmt.Errorf("failed to wait for page load: %w", err)
	}

	return nil
}

func (bc *BlogCrawler) waitForContent() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Wait for initial content to load
	return bc.page.Context(ctx).WaitStable(time.Millisecond * 500)
}

func (bc *BlogCrawler) normalizeURL(href string, keepQueryParams bool) (string, error) {
	// Parse base URL to get scheme and host
	baseURLParsed, err := url.Parse(bc.baseURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse base URL: %w", err)
	}

	// Parse the href
	hrefParsed, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("failed to parse href: %w", err)
	}

	// Resolve relative URLs
	absoluteURL := baseURLParsed.ResolveReference(hrefParsed)

	// Remove query parameters and fragments only if requested
	if !keepQueryParams {
		absoluteURL.RawQuery = ""
	}
	absoluteURL.Fragment = ""

	return absoluteURL.String(), nil
}

func (bc *BlogCrawler) extractBlogURLs() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try multiple selectors to catch different blog layouts
	// Priority: Uber-specific first, then generic
	selectors := []string{
		`a[data-baseweb="card"][href]`,         // Uber blog posts (specific)
		"article a[href]",                      // Links in articles
		"h2 a[href]",                           // Links in h2 headings
		"h3 a[href]",                           // Links in h3 headings
		"[data-testid='post-preview-title'] a", // Medium specific
		".post-title a",                        // Generic post title
		".blog-post a",                         // Generic blog post
		"a[href]",                              // All links (fallback)
	}

	urlSet := make(map[string]bool)

	// Parse base URL to get domain for filtering
	baseURLParsed, err := url.Parse(bc.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base URL: %w", err)
	}
	baseDomain := baseURLParsed.Host

	for _, selector := range selectors {
		elements, err := bc.page.Context(ctx).Elements(selector)
		if err != nil {
			continue // Try next selector if this one fails
		}

		for _, elem := range elements {
			href, err := elem.Attribute("href")
			if err != nil || href == nil {
				continue
			}

			// For Uber blog posts, keep query parameters (like ?uclick_id=...)
			// For other sites, strip them
			keepQueryParams := strings.Contains(bc.baseURL, "uber.com")
			normalizedURL, err := bc.normalizeURL(*href, keepQueryParams)
			if err != nil {
				continue
			}

			// Parse normalized URL to check domain
			parsedURL, err := url.Parse(normalizedURL)
			if err != nil {
				continue
			}

			// Filter to only include URLs from the same domain
			if parsedURL.Host == baseDomain || parsedURL.Host == "" {
				// Skip non-blog URLs (like /about, /archive, etc.)
				if bc.isBlogPostURL(normalizedURL) {
					urlSet[normalizedURL] = true
				}
			}
		}
	}

	urls := make([]string, 0, len(urlSet))
	for url := range urlSet {
		urls = append(urls, url)
	}

	return urls, nil
}

func (bc *BlogCrawler) isBlogPostURL(urlStr string) bool {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	path := strings.ToLower(parsedURL.Path)
	urlLower := strings.ToLower(urlStr)

	// Parse base URL to get base path
	baseURLParsed, err := url.Parse(bc.baseURL)
	if err != nil {
		return false
	}
	basePath := strings.ToLower(baseURLParsed.Path)

	// For LinkedIn blog: check if it's a blog post URL pattern
	// Pattern: /blog/engineering/<category>/<post-slug> or similar
	if strings.Contains(bc.baseURL, "linkedin.com") {
		// LinkedIn blog posts typically have paths like /blog/engineering/data/...
		// Exclude pagination query params
		if strings.Contains(path, "?page0=") {
			return false // Pagination page
		}
		// Include if it matches /blog/engineering/<category>/<post-slug> pattern
		if strings.HasPrefix(path, "/blog/engineering/") {
			// Get the part after /blog/engineering/
			blogPath := strings.TrimPrefix(path, "/blog/engineering/")
			blogPath = strings.Trim(blogPath, "/")
			parts := strings.Split(blogPath, "/")

			// Exclude known category pages (data, infrastructure)
			categories := []string{"data", "infrastructure"}
			if len(parts) == 1 && contains(categories, parts[0]) {
				return false // Category page
			}

			// If it has more than just the category, it's likely a blog post
			if len(parts) > 1 {
				return true
			}
		}
		return false
	}

	// For Uber blog: check if it's a blog post URL pattern
	// Pattern: /blog/<post-slug>/ or /blog/<category>/<post-slug>/
	if strings.Contains(bc.baseURL, "uber.com") {
		// Uber blog posts follow pattern: /blog/<slug>/
		// Exclude pagination, category pages, etc.
		if strings.Contains(path, "/page/") {
			return false // Pagination page
		}
		if strings.Contains(path, "/engineering/backend/page/") {
			return false // Pagination page
		}
		// Include if it matches /blog/<something>/ pattern and is not a category
		if strings.HasPrefix(path, "/blog/") {
			// Get the part after /blog/
			blogPath := strings.TrimPrefix(path, "/blog/")
			blogPath = strings.Trim(blogPath, "/")
			parts := strings.Split(blogPath, "/")

			// Exclude known category pages
			categories := []string{"engineering", "advertising", "earn", "ride", "eat", "merchants",
				"business", "freight", "health", "higher-education", "transit", "careers",
				"community-support", "research"}
			if len(parts) == 1 && contains(categories, parts[0]) {
				return false // Category page
			}

			// If it has a slug (not just a category), it's likely a blog post
			if len(parts) > 0 && parts[0] != "" {
				// Check if it's a category with subcategory (like /blog/engineering/backend/)
				if len(parts) == 2 && parts[0] == "engineering" {
					// This is a category listing page, not a post
					return false
				}
				// Otherwise, it's likely a blog post
				return true
			}
		}
		return false
	}

	// Filter out common non-blog URLs for other sites
	excludePatterns := []string{
		"/about",
		"/archive",
		"/tag/",
		"/search",
		"/@",
		"/latest",
		"/membership",
		"/settings",
		"/me/",
		"/?source=",
		"/page/", // Pagination pages
		"/category/",
		"/categories/",
		"/author/",
		"/authors/",
		"/feed",
		"/rss",
		"/sitemap",
		"/contact",
		"/privacy",
		"/terms",
		"/careers",
	}

	for _, pattern := range excludePatterns {
		if strings.Contains(urlLower, pattern) {
			// Some patterns like "/p/" might be blog posts, so check more carefully
			if pattern == "/p/" && strings.Count(path, "/") >= 4 {
				// Likely a blog post: /username/post-title-123456
				continue
			}
			return false
		}
	}

	// Get relative path
	relativePath := strings.TrimPrefix(path, basePath)
	relativePath = strings.Trim(relativePath, "/")

	// Exclude if it's just the base path or empty
	if relativePath == "" || relativePath == "/" {
		return false
	}

	// Exclude language codes and pagination in path
	pathParts := strings.Split(relativePath, "/")
	for _, part := range pathParts {
		// Skip language codes (en-US, es-US, etc.)
		if strings.Contains(part, "-us") || (strings.Contains(part, "-") && len(part) <= 6) {
			continue
		}
		// Skip pagination
		if part == "page" {
			return false
		}
	}

	// Include URLs that look like blog posts
	// Should have at least one meaningful path segment after the base
	if len(pathParts) > 0 && pathParts[0] != "" {
		// Check if it contains typical blog post indicators
		if strings.Contains(path, "/blog/") ||
			strings.Contains(path, "/post/") ||
			strings.Contains(path, "/article/") ||
			(len(pathParts) >= 2 && pathParts[0] == "blog") {
			return true
		}
		// For other sites: if it's a direct path under base, it's likely a post
		if strings.HasPrefix(path, basePath) && len(pathParts) >= 1 {
			return true
		}
	}

	return false
}

// Helper function to check if a string is in a slice
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func (bc *BlogCrawler) scrollToBottom() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use a more robust scrolling method
	_, err := bc.page.Context(ctx).Eval(`
		(function() {
			window.scrollTo({
				top: document.body.scrollHeight || document.documentElement.scrollHeight,
				behavior: 'smooth'
			});
		})()
	`)
	return err
}

func (bc *BlogCrawler) getMaxPageNumber() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First, try to find pagination select dropdown (Uber uses this)
	selectElements, err := bc.page.Context(ctx).Elements(`[data-baseweb="select"] div[value]`)
	if err == nil && len(selectElements) > 0 {
		maxPage := 0
		for _, elem := range selectElements {
			value, err := elem.Attribute("value")
			if err != nil || value == nil {
				continue
			}
			if pageNum, err := strconv.Atoi(*value); err == nil && pageNum > maxPage {
				maxPage = pageNum
			}
		}
		if maxPage > 0 {
			return maxPage, nil
		}
	}

	// Alternative: Look for pagination links and find the highest page number
	elements, err := bc.page.Context(ctx).Elements(`a[href*="/page/"]`)
	if err == nil {
		maxPage := 0
		for _, elem := range elements {
			href, err := elem.Attribute("href")
			if err != nil || href == nil {
				continue
			}
			// Extract page number from href like "/blog/engineering/backend/page/3/"
			if strings.Contains(*href, "/page/") {
				parts := strings.Split(*href, "/page/")
				if len(parts) > 1 {
					pagePart := strings.Trim(parts[1], "/")
					pageNumStr := strings.Split(pagePart, "/")[0]
					if pageNum, err := strconv.Atoi(pageNumStr); err == nil && pageNum > maxPage {
						maxPage = pageNum
					}
				}
			}
		}
		if maxPage > 0 {
			// Add 1 to account for the fact that we might be on page 1
			// and the highest link might be to the last page
			return maxPage, nil
		}
	}

	// Try to get text content and look for "Page X of Y"
	// Look for the pagination text element directly
	paginationText, err := bc.page.Context(ctx).Eval(`
		(function() {
			// Look for element containing "Page X of Y" text
			const allElements = document.querySelectorAll('*');
			for (let el of allElements) {
				const text = el.textContent || el.innerText || '';
				const match = text.match(/Page\s+(\d+)\s+of\s+(\d+)/i);
				if (match) {
					return parseInt(match[2]);
				}
			}
			return 0;
		})()
	`)
	if err == nil {
		maxPageStr := fmt.Sprintf("%v", paginationText.Value)
		if maxPage, err := strconv.Atoi(maxPageStr); err == nil && maxPage > 0 {
			return maxPage, nil
		}
	}

	return 0, fmt.Errorf("could not determine max page number")
}

func (bc *BlogCrawler) crawlSinglePage(pageURL string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), bc.timeout)
	defer cancel()

	// Navigate to the page
	if err := bc.page.Context(ctx).Navigate(pageURL); err != nil {
		return nil, fmt.Errorf("failed to navigate to %s: %w", pageURL, err)
	}

	if err := bc.page.Context(ctx).WaitLoad(); err != nil {
		return nil, fmt.Errorf("failed to wait for page load: %w", err)
	}

	// Wait for content to load
	if err := bc.waitForContent(); err != nil {
		fmt.Printf("Warning: Timeout waiting for content on %s: %v\n", pageURL, err)
	}

	// Extract blog URLs from this page
	return bc.extractBlogURLs()
}

func (bc *BlogCrawler) crawl() (*CrawlResult, error) {
	fmt.Printf("Initializing browser...\n")
	if err := bc.initializeBrowser(); err != nil {
		return nil, err
	}
	defer bc.browser.Close()

	fmt.Printf("Navigating to %s...\n", bc.baseURL)
	if err := bc.navigateToPage(); err != nil {
		return nil, err
	}

	fmt.Printf("Waiting for content to load...\n")
	if err := bc.waitForContent(); err != nil {
		fmt.Printf("Warning: Timeout waiting for initial content: %v\n", err)
	}

	// Check if this is a paginated blog (like Uber or LinkedIn)
	isUberBlog := strings.Contains(bc.baseURL, "uber.com")
	isLinkedInBlog := strings.Contains(bc.baseURL, "linkedin.com/blog")
	urlSet := make(map[string]bool)

	if isLinkedInBlog && (strings.Contains(bc.baseURL, "/blog/engineering/data") || strings.Contains(bc.baseURL, "/blog/engineering/infrastructure")) {
		// LinkedIn blog with pagination - extract actual pagination links from the page
		fmt.Printf("Detected LinkedIn blog with pagination. Extracting pagination pattern...\n")

		// Extract base URL without query params
		baseURLParsed, err := url.Parse(bc.baseURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse base URL: %w", err)
		}
		baseURLParsed.RawQuery = ""
		basePath := baseURLParsed.String()

		// Try to extract pagination links from the current page to understand the pattern
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		paginationLinks := make(map[int]string) // page number -> URL

		// Look for pagination links with page0 parameter
		elements, err := bc.page.Context(ctx).Elements(`a[href*="page0="]`)
		if err == nil {
			for _, elem := range elements {
				href, err := elem.Attribute("href")
				if err != nil || href == nil {
					continue
				}
				// Parse the href to extract page0 value
				parsedHref, err := url.Parse(*href)
				if err == nil {
					page0Value := parsedHref.Query().Get("page0")
					if page0Value != "" {
						if pageNum, err := strconv.Atoi(page0Value); err == nil {
							// Resolve relative URL
							absoluteURL := baseURLParsed.ResolveReference(parsedHref)
							absoluteURL.RawQuery = fmt.Sprintf("page0=%s", page0Value)
							paginationLinks[pageNum] = absoluteURL.String()
						}
					}
				}
			}
		}
		cancel()

		// LinkedIn pagination pattern: page0 is a fixed parameter name, value is the page number
		// Page 1: no query param (or ?page0=1)
		// Page 2: ?page0=2
		// Page 3: ?page0=3
		// etc.
		// We'll try both: start with no param for page 1, then use sequential page numbers
		fmt.Printf("Using LinkedIn pagination pattern: page0=<page_number> (sequential: 1, 2, 3, ...)\n")

		consecutiveEmptyPages := 0
		maxConsecutiveEmpty := 1 // Stop on first empty page
		pageNum := 1

		for {
			var pageURL string
			if pageNum == 1 {
				pageURL = basePath // First page: no query param
			} else {
				pageURL = fmt.Sprintf("%s?page0=%d", basePath, pageNum)
			}

			fmt.Printf("Crawling page %d: %s\n", pageNum, pageURL)

			urls, err := bc.crawlSinglePage(pageURL)
			if err != nil {
				fmt.Printf("Warning: Error crawling page %d: %v\n", pageNum, err)
				consecutiveEmptyPages++
				if consecutiveEmptyPages >= maxConsecutiveEmpty {
					fmt.Printf("Stopping: Error on page %d\n", pageNum)
					break
				}
				continue
			}

			if len(urls) == 0 {
				consecutiveEmptyPages++
				if consecutiveEmptyPages >= maxConsecutiveEmpty {
					fmt.Printf("Stopping: No blog posts found on page %d\n", pageNum)
					break
				}
			} else {
				consecutiveEmptyPages = 0
				previousCount := len(urlSet)
				for _, url := range urls {
					urlSet[url] = true
				}
				fmt.Printf("  Found %d blog URLs on page %d (total: %d unique URLs)\n", len(urls), pageNum, len(urlSet))

				// If no new URLs were added, we might have reached the end
				if len(urlSet) == previousCount {
					consecutiveEmptyPages++
					if consecutiveEmptyPages >= maxConsecutiveEmpty {
						fmt.Printf("Stopping: No new URLs found on page %d\n", pageNum)
						break
					}
				}
			}

			// Safety limit: don't go beyond 50 pages
			if pageNum >= 50 {
				fmt.Printf("Reached safety limit of 50 pages. Stopping.\n")
				break
			}

			pageNum++
			time.Sleep(1 * time.Second)
		}
	} else if isUberBlog && strings.Contains(bc.baseURL, "/blog/engineering/backend") {
		// Uber blog with pagination - simple increment approach
		fmt.Printf("Detected Uber blog with pagination. Crawling all pages...\n")

		// Extract base path without page number
		basePath := strings.TrimSuffix(bc.baseURL, "/")
		if strings.Contains(basePath, "/page/") {
			// Remove /page/X from the end
			basePath = strings.Split(basePath, "/page/")[0]
		}
		basePath = strings.TrimSuffix(basePath, "/")

		pageNum := 1
		consecutiveEmptyPages := 0
		maxConsecutiveEmpty := 1 // Stop on first empty page

		for {
			var pageURL string
			if pageNum == 1 {
				pageURL = basePath + "/"
			} else {
				pageURL = fmt.Sprintf("%s/page/%d/", basePath, pageNum)
			}

			fmt.Printf("Crawling page %d: %s\n", pageNum, pageURL)

			urls, err := bc.crawlSinglePage(pageURL)
			if err != nil {
				fmt.Printf("Warning: Error crawling page %d: %v\n", pageNum, err)
				consecutiveEmptyPages++
				if consecutiveEmptyPages >= maxConsecutiveEmpty {
					fmt.Printf("Stopping: Error on page %d\n", pageNum)
					break
				}
				pageNum++
				continue
			}

			if len(urls) == 0 {
				consecutiveEmptyPages++
				if consecutiveEmptyPages >= maxConsecutiveEmpty {
					fmt.Printf("Stopping: No blog posts found on page %d\n", pageNum)
					break
				}
			} else {
				consecutiveEmptyPages = 0
				previousCount := len(urlSet)
				for _, url := range urls {
					urlSet[url] = true
				}
				fmt.Printf("  Found %d blog URLs on page %d (total: %d unique URLs)\n", len(urls), pageNum, len(urlSet))

				// If no new URLs were added, we might have reached the end
				if len(urlSet) == previousCount {
					consecutiveEmptyPages++
					if consecutiveEmptyPages >= maxConsecutiveEmpty {
						fmt.Printf("Stopping: No new URLs found on page %d\n", pageNum)
						break
					}
				}
			}

			// Safety limit: don't go beyond 20 pages
			if pageNum >= 20 {
				fmt.Printf("Reached safety limit of 20 pages. Stopping.\n")
				break
			}

			pageNum++
			time.Sleep(1 * time.Second)
		}
	} else {
		// Original behavior: scroll and extract (for Medium and other blogs)
		fmt.Printf("Starting to crawl blog URLs (infinite scroll mode)...\n")

		noNewContentCount := 0
		maxNoNewContentIterations := 3
		scrollDelay := 2 * time.Second

		for {
			// Extract current URLs
			currentURLs, err := bc.extractBlogURLs()
			if err != nil {
				fmt.Printf("Warning: Error extracting URLs: %v\n", err)
			} else {
				previousCount := len(urlSet)
				for _, url := range currentURLs {
					urlSet[url] = true
				}
				newCount := len(urlSet)

				fmt.Printf("Found %d unique blog URLs so far...\n", newCount)

				if newCount == previousCount {
					noNewContentCount++
					if noNewContentCount >= maxNoNewContentIterations {
						fmt.Printf("No new content detected after %d scrolls. Stopping.\n", maxNoNewContentIterations)
						break
					}
				} else {
					noNewContentCount = 0
				}
			}

			// Scroll down
			if err := bc.scrollToBottom(); err != nil {
				fmt.Printf("Warning: Error scrolling: %v\n", err)
			}

			// Wait for new content to load
			time.Sleep(scrollDelay)

			// Small delay to allow content to load
			time.Sleep(500 * time.Millisecond)
		}
	}

	urls := make([]string, 0, len(urlSet))
	for url := range urlSet {
		urls = append(urls, url)
	}

	return &CrawlResult{
		BaseURL:    bc.baseURL,
		BlogURLs:   urls,
		TotalCount: len(urls),
		CrawledAt:  time.Now().Format(time.RFC3339),
	}, nil
}

func (bc *BlogCrawler) saveToJSON(result *CrawlResult, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}

	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <base_url> [output_file.json]")
		fmt.Println("Example: go run main.go https://medium.com/netflix-techblog")
		os.Exit(1)
	}

	baseURL := os.Args[1]
	outputFile := "blog_urls.json"
	if len(os.Args) >= 3 {
		outputFile = os.Args[2]
	}

	// 30 second timeout for initial page load
	timeout := 30 * time.Second

	crawler := NewBlogCrawler(baseURL, timeout)

	fmt.Printf("Starting blog crawler for: %s\n", baseURL)
	fmt.Printf("Timeout set to: %v\n", timeout)

	result, err := crawler.crawl()
	if err != nil {
		fmt.Printf("Error during crawling: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nCrawling completed!\n")
	fmt.Printf("Total blog URLs found: %d\n", result.TotalCount)

	if err := crawler.saveToJSON(result, outputFile); err != nil {
		fmt.Printf("Error saving to JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Results saved to: %s\n", outputFile)
}
