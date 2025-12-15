# Manual Blog Crawler

A Go-based blog crawler using Rod for browser automation. This tool crawls blog websites (like Medium) to extract all blog post URLs by automatically scrolling through the page.

## Features

- 🕷️ Automated browser crawling using Rod
- 📜 Infinite scroll support - automatically scrolls until no new content appears
- 🔗 Extracts all blog post URLs from the page
- 💾 Saves results to JSON file
- ⏱️ Timeout handling with error messages
- 🎯 Smart URL filtering to exclude non-blog pages

## Installation

1. Make sure you have Go installed (version 1.16 or later)
2. Install dependencies:
```bash
go mod download
```

## Usage

```bash
go run main.go <base_url> [output_file.json]
```

### Examples

```bash
# Crawl Medium blog and save to default blog_urls.json
go run main.go https://medium.com/netflix-techblog

# Crawl and save to custom file
go run main.go https://medium.com/netflix-techblog results.json
```

## Output Format

The crawler generates a JSON file with the following structure:

```json
{
  "base_url": "https://medium.com/netflix-techblog",
  "blog_urls": [
    "https://medium.com/netflix-techblog/post-1",
    "https://medium.com/netflix-techblog/post-2"
  ],
  "total_count": 2,
  "crawled_at": "2024-01-01T12:00:00Z"
}
```

## How It Works

1. **Browser Initialization**: Launches a headless browser using Rod
2. **Page Navigation**: Navigates to the provided base URL
3. **Content Loading**: Waits for initial content to load
4. **Scrolling**: Automatically scrolls to the bottom of the page
5. **URL Extraction**: Extracts blog post URLs using multiple CSS selectors
6. **Content Detection**: Monitors for new content - stops when no new URLs appear after 3 scroll iterations
7. **JSON Export**: Saves all unique blog URLs to a JSON file

## Timeout Handling

- Initial page load timeout: 30 seconds
- If a timeout occurs, the crawler will stop and print an error message
- Individual operations have their own timeout handlers

## Notes

- The crawler runs in headless mode (no visible browser window)
- It filters out non-blog URLs (like /about, /archive, etc.)
- Medium-specific selectors are included for better compatibility
- The crawler waits between scrolls to allow content to load

