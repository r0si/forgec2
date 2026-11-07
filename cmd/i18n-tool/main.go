package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/forgec2/forgec2/internal/server"
)

func main() {
	checkCmd := flag.NewFlagSet("check", flag.ExitOnError)
	checkLang := checkCmd.String("lang", "", "Language to check (default: all)")

	statsCmd := flag.NewFlagSet("stats", flag.ExitOnError)

	exportCmd := flag.NewFlagSet("export", flag.ExitOnError)
	exportLang := exportCmd.String("lang", "en", "Language to export")
	exportFormat := exportCmd.String("format", "json", "Export format (json)")
	exportOutput := exportCmd.String("output", "", "Output file path (default: stdout)")

	listCmd := flag.NewFlagSet("list", flag.ExitOnError)

	missingCmd := flag.NewFlagSet("missing", flag.ExitOnError)
	missingLang := missingCmd.String("lang", "", "Language to check for missing translations")

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "check":
		_ = checkCmd.Parse(os.Args[2:])
		runCheck(*checkLang)
	case "stats":
		_ = statsCmd.Parse(os.Args[2:])
		runStats()
	case "export":
		_ = exportCmd.Parse(os.Args[2:])
		runExport(*exportLang, *exportFormat, *exportOutput)
	case "list":
		_ = listCmd.Parse(os.Args[2:])
		runList()
	case "missing":
		_ = missingCmd.Parse(os.Args[2:])
		runMissing(*missingLang)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`ForgeC2 i18n Translation Management Tool

Usage:
  i18n-tool <command> [options]

Commands:
  check     Check translation quality (placeholders, HTML tags)
  stats     Show translation statistics
  export    Export translations to a file
  list      List all supported languages
  missing   List missing translations
  help      Show this help message

Examples:
  i18n-tool check --lang zh
  i18n-tool stats
  i18n-tool export --lang zh --output zh.json
  i18n-tool missing --lang zh
`)
}

func runCheck(lang string) {
	fmt.Println("=== Translation Quality Check ===")
	fmt.Println()

	langs := server.GetSupportedLanguages()
	var langCodes []string

	if lang != "" {
		if !server.IsLanguageSupported(lang) {
			fmt.Fprintf(os.Stderr, "Error: Unsupported language: %s\n", lang)
			os.Exit(1)
		}
		langCodes = []string{lang}
	} else {
		for code := range langs {
			langCodes = append(langCodes, code)
		}
		sort.Strings(langCodes)
	}

	hasIssues := false

	for _, code := range langCodes {
		info, _ := server.GetLanguageInfo(code)
		fmt.Printf("Language: %s (%s) [%s]\n", info.NativeName, info.Name, code)
		fmt.Println(strings.Repeat("-", 60))

		placeholderIssues := server.CheckPlaceholderConsistency("en", code)
		if len(placeholderIssues) > 0 {
			hasIssues = true
			fmt.Printf("  Placeholder issues: %d\n", len(placeholderIssues))
			keys := make([]string, 0, len(placeholderIssues))
			for k := range placeholderIssues {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("    - %s:\n", k)
				for _, msg := range placeholderIssues[k] {
					fmt.Printf("      %s\n", msg)
				}
			}
		} else {
			fmt.Println("  Placeholder check: PASSED ✓")
		}

		htmlIssues := server.CheckHTMLTags(code)
		if len(htmlIssues) > 0 {
			hasIssues = true
			fmt.Printf("  HTML tag issues: %d\n", len(htmlIssues))
			keys := make([]string, 0, len(htmlIssues))
			for k := range htmlIssues {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("    - %s: %s\n", k, htmlIssues[k])
			}
		} else {
			fmt.Println("  HTML tag check: PASSED ✓")
		}

		missing := server.GetMissingTranslations(code)
		if len(missing) > 0 {
			hasIssues = true
			fmt.Printf("  Missing translations: %d\n", len(missing))
		} else {
			fmt.Println("  Missing translations: NONE ✓")
		}

		fmt.Println()
	}

	if hasIssues {
		fmt.Println("⚠️  Some issues found. Please review the output above.")
	} else {
		fmt.Println("✅ All checks passed!")
	}
}

func runStats() {
	fmt.Println("=== Translation Statistics ===")
	fmt.Println()

	stats := server.GetTranslationStats()
	langs := server.GetSupportedLanguages()
	allKeys := server.GetAllTranslationKeys()
	totalKeys := len(allKeys)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LANGUAGE\tNATIVE NAME\tTRANSLATIONS\tCOMPLETENESS")
	fmt.Fprintln(w, strings.Repeat("-", 8)+"\t"+strings.Repeat("-", 12)+"\t"+strings.Repeat("-", 12)+"\t"+strings.Repeat("-", 12))

	var langCodes []string
	for code := range langs {
		langCodes = append(langCodes, code)
	}
	sort.Strings(langCodes)

	for _, code := range langCodes {
		info := langs[code]
		count := stats[code]
		var pct float64
		if totalKeys > 0 {
			pct = float64(count) / float64(totalKeys) * 100
		}
		rtlMark := ""
		if info.RTL {
			rtlMark = " [RTL]"
		}
		fmt.Fprintf(w, "%s\t%s\t%d / %d\t%.1f%%%s\n",
			code, info.NativeName, count, totalKeys, pct, rtlMark)
	}

	w.Flush()
	fmt.Println()
	fmt.Printf("Total translation keys: %d\n", totalKeys)
	fmt.Printf("Supported languages: %d\n", len(langs))
}

func runExport(lang, format, output string) {
	if !server.IsLanguageSupported(lang) {
		fmt.Fprintf(os.Stderr, "Error: Unsupported language: %s\n", lang)
		os.Exit(1)
	}

	translations, err := server.ExportTranslations(lang)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exporting translations: %v\n", err)
		os.Exit(1)
	}

	var data []byte
	switch format {
	case "json":
		data, err = json.MarshalIndent(translations, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting JSON: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Error: Unsupported format: %s\n", format)
		os.Exit(1)
	}

	if output != "" {
		err = os.WriteFile(output, data, 0600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Exported %d translations to %s\n", len(translations), output)
	} else {
		fmt.Println(string(data))
	}
}

func runList() {
	fmt.Println("=== Supported Languages ===")
	fmt.Println()

	langs := server.GetSupportedLanguages()
	var langCodes []string
	for code := range langs {
		langCodes = append(langCodes, code)
	}
	sort.Strings(langCodes)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CODE\tNAME\tNATIVE NAME\tRTL\tFLAG")
	fmt.Fprintln(w, strings.Repeat("-", 4)+"\t"+strings.Repeat("-", 10)+"\t"+strings.Repeat("-", 12)+"\t"+strings.Repeat("-", 3)+"\t"+strings.Repeat("-", 4))

	for _, code := range langCodes {
		info := langs[code]
		rtlStr := "No"
		if info.RTL {
			rtlStr = "Yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			code, info.Name, info.NativeName, rtlStr, info.Flag)
	}

	w.Flush()
	fmt.Println()
	fmt.Printf("Total: %d languages\n", len(langs))
}

func runMissing(lang string) {
	fmt.Println("=== Missing Translations ===")
	fmt.Println()

	langs := server.GetSupportedLanguages()
	var langCodes []string

	if lang != "" {
		if !server.IsLanguageSupported(lang) {
			fmt.Fprintf(os.Stderr, "Error: Unsupported language: %s\n", lang)
			os.Exit(1)
		}
		langCodes = []string{lang}
	} else {
		for code := range langs {
			langCodes = append(langCodes, code)
		}
		sort.Strings(langCodes)
	}

	totalMissing := 0

	for _, code := range langCodes {
		info, _ := server.GetLanguageInfo(code)
		missing := server.GetMissingTranslations(code)

		if len(missing) > 0 {
			totalMissing += len(missing)
			fmt.Printf("%s (%s) - %d missing:\n", info.NativeName, code, len(missing))
			sort.Strings(missing)
			for _, k := range missing {
				fmt.Printf("  - %s\n", k)
			}
			fmt.Println()
		} else {
			fmt.Printf("%s (%s) - No missing translations ✓\n\n", info.NativeName, code)
		}
	}

	if totalMissing == 0 {
		fmt.Println("✅ All translations are complete!")
	} else {
		fmt.Printf("Total missing translations: %d\n", totalMissing)
	}
}
