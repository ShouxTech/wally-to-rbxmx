package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
)

type StringProperty struct {
	XMLName xml.Name `xml:"string"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:",chardata"`
}
type BoolProperty struct {
	XMLName xml.Name `xml:"bool"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:",chardata"`
}
type Int64Property struct {
	XMLName xml.Name `xml:"int64"`
	Name    string   `xml:"name,attr"`
	Value   int64    `xml:",chardata"`
}
type BinaryStringProperty struct {
	XMLName xml.Name `xml:"BinaryString"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:",chardata"`
}
type ProtectedStringProperty struct {
	XMLName xml.Name `xml:"ProtectedString"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:",cdata"`
}
type ContentProperty struct {
	XMLName xml.Name  `xml:"Content"`
	Name    string    `xml:"name,attr"`
	Null    *struct{} `xml:"null"`
}
type SecurityCapabilitiesProperty struct {
	XMLName xml.Name `xml:"SecurityCapabilities"`
	Name    string   `xml:"name,attr"`
	Value   int      `xml:",chardata"`
}
type Item struct {
	XMLName    xml.Name `xml:"Item"`
	Class      string   `xml:"class,attr"`
	Referent   string   `xml:"referent,attr"`
	Properties []any    `xml:"Properties>any"`
	Children   []*Item  `xml:",any"`
}
type Roblox struct {
	XMLName                      xml.Name `xml:"roblox"`
	XmlnsXsi                     string   `xml:"xmlns:xsi,attr"`
	XsiNoNamespaceSchemaLocation string   `xml:"xsi:noNamespaceSchemaLocation,attr"`
	Version                      string   `xml:"version,attr"`
	Meta                         *Meta    `xml:"Meta"`
	Externals                    []string `xml:"External"`
	Items                        []*Item  `xml:"Item"`
}
type Meta struct {
	XMLName xml.Name `xml:"Meta"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:",chardata"`
}

type PackageIdentifier struct{ Scope, Name, Version string }

func (p PackageIdentifier) String() string {
	return fmt.Sprintf("%s/%s@%s", p.Scope, p.Name, p.Version)
}

type WallyPackageInfo struct{ Name, Version, Realm string }
type WallyToml struct {
	Package      WallyPackageInfo
	Dependencies map[string]string
}

var packageIdentifierRegex = regexp.MustCompile(`^([a-zA-Z0-9_-]+)/([a-zA-Z0-9_-]+)@([a-zA-Z0-9._+-]+)$`)
var dependencyRegex = regexp.MustCompile(`^([a-zA-Z0-9_-]+)/([a-zA-Z0-9_-]+)(?:@(.*))?$`)
var versionExtractRegex = regexp.MustCompile(`([\d.]+[a-zA-Z0-9._+-]*)`)
var processedPackages = make(map[string]bool)
var processedMutex sync.Mutex

const DEFAULT_OUTPUT_NAME = "output.rbxmx"
const SIMULATED_WALLY_VERSION = "0.3.2"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: wally-to-rbxmx <scope/name@version> [output.rbxmx]")
		return
	}
	initialPackageIdentifierStr := os.Args[1]
	log.Printf("Processing package identifier: %s", initialPackageIdentifierStr)

	initialScope, initialName, initialVersion, err := parsePackageIdentifier(initialPackageIdentifierStr)
	if err != nil {
		log.Fatalf("Error parsing package identifier '%s': %v", initialPackageIdentifierStr, err)
	}
	initialPkg := PackageIdentifier{Scope: initialScope, Name: initialName, Version: initialVersion}

	outFilename := DEFAULT_OUTPUT_NAME
	if len(os.Args) >= 3 {
		outFilename = os.Args[2]
		log.Printf("Output filename specified: %s", outFilename)
	} else {
		outFilename = fmt.Sprintf("%s_%s@%s.rbxmx", initialPkg.Scope, initialPkg.Name, initialPkg.Version)
		log.Printf("Output file will be default: %s", outFilename)
	}

	log.Printf("--- Starting processing for root package: %s ---", initialPkg.String())
	processedMutex.Lock()
	processedPackages = make(map[string]bool)
	processedMutex.Unlock()

	allRootItems, err := processPackageAndDependencies(initialPkg)
	if err != nil {
		log.Printf("Warning: Encountered errors during dependency processing: %v", err)
		if len(allRootItems) == 0 {
			log.Fatalf("No items generated due to processing errors.")
		}
	}
	log.Println("--- Finished processing all packages and dependencies ---")

	if len(allRootItems) == 0 {
		log.Println("Warning: No Lua(u) files found or processed.")
	}

	log.Println("Generating final RBXMX structure...")
	rbxmxContent, err := generateRBXMX(allRootItems)
	if err != nil {
		log.Fatalf("Error generating RBXMX: %v", err)
	}
	log.Println("RBXMX generation complete.")

	log.Printf("Writing RBXMX to %s...", outFilename)
	err = os.WriteFile(outFilename, rbxmxContent, 0644)
	if err != nil {
		log.Fatalf("Error writing output file: %v", err)
	}

	log.Printf("Successfully converted package '%s' and its dependencies to '%s'", initialPackageIdentifierStr, outFilename)
}

func processPackageAndDependencies(pkg PackageIdentifier) ([]*Item, error) {
	pkgStr := pkg.String()

	processedMutex.Lock()
	alreadyProcessed := processedPackages[pkgStr]
	if !alreadyProcessed {
		processedPackages[pkgStr] = true
	}
	processedMutex.Unlock()

	if alreadyProcessed {
		log.Printf("Skipping already processed package: %s", pkgStr)
		return []*Item{}, nil
	}

	log.Printf("Processing package: %s", pkgStr)
	downloadURL := fmt.Sprintf("https://api.wally.run/v1/package-contents/%s/%s/%s", pkg.Scope, pkg.Name, pkg.Version)
	log.Printf("Constructed download URL: %s", downloadURL)

	zipData, err := downloadZip(downloadURL)
	if err != nil {
		log.Printf("Error downloading zip for %s: %v. Skipping this package.", pkgStr, err)
		return []*Item{}, fmt.Errorf("Failed to download %s: %w", pkgStr, err)
	}

	rootModuleName := fmt.Sprintf("%s_%s@%s", pkg.Scope, pkg.Name, pkg.Version)
	currentPackageItems, discoveredDeps, err := processSingleZipArchive(bytes.NewReader(zipData), int64(len(zipData)), rootModuleName, pkg.Name, pkg.Version)
	if err != nil {
		log.Printf("Error processing zip archive for %s: %v. Skipping this package's items.", pkgStr, err)
		currentPackageItems = []*Item{}
	}

	aggregatedItems := currentPackageItems
	var depErrors []error
	log.Printf("Found %d dependencies for %s", len(discoveredDeps), pkgStr)
	for depAlias, depIdentifier := range discoveredDeps {
		log.Printf("--> Processing dependency '%s': %s", depAlias, depIdentifier.String())
		depItems, depErr := processPackageAndDependencies(depIdentifier)
		if depErr != nil {
			log.Printf("----> Failed to process dependency %s: %v", depIdentifier.String(), depErr)
			depErrors = append(depErrors, depErr)
		} else {
			log.Printf("----> Finished processing dependency %s, found %d root items.", depIdentifier.String(), len(depItems))
			aggregatedItems = append(aggregatedItems, depItems...)
		}
	}

	var combinedErr error
	if err != nil {
		depErrors = append(depErrors, fmt.Errorf("Failed processing self %s: %w", pkgStr, err))
	}
	if len(depErrors) > 0 {
		errorStrings := make([]string, len(depErrors))
		for i, e := range depErrors {
			errorStrings[i] = e.Error()
		}
		combinedErr = errors.New("Errors encountered: " + strings.Join(errorStrings, "; "))
	}

	log.Printf("Finished processing %s, total items generated including dependencies: %d", pkgStr, len(aggregatedItems))
	return aggregatedItems, combinedErr
}

func processSingleZipArchive(reader io.ReaderAt, size int64, rootModuleName, basePackageName, basePackageVersion string) ([]*Item, map[string]PackageIdentifier, error) {
	zipReader, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to open zip reader: %w", err)
	}

	var rootDirPrefix string
	potentialPrefix := fmt.Sprintf("%s-%s/", basePackageName, basePackageVersion)
	potentialPrefixV := fmt.Sprintf("%s-v%s/", basePackageName, basePackageVersion)
	potentialPrefixWally := fmt.Sprintf("%s-%s/", basePackageName, strings.ReplaceAll(basePackageVersion, ".", "_"))
	foundPrefix := false
	checkPrefixes := []string{potentialPrefix, potentialPrefixV, potentialPrefixWally}
	dirEntries := make(map[string]bool)

	for _, f := range zipReader.File {
		if strings.HasSuffix(f.Name, "/") {
			dirEntries[f.Name] = true
		}
		for _, prefix := range checkPrefixes {
			if strings.HasPrefix(f.Name, prefix) && strings.HasSuffix(f.Name, "/") {
				rootDirPrefix = prefix
				foundPrefix = true
				log.Printf("Detected package root directory in zip using pattern: %s", rootDirPrefix)
				break
			}
		}
		if foundPrefix {
			break
		}
	}

	if !foundPrefix {
		topLevelDirs := make(map[string]bool)
		for _, f := range zipReader.File {
			if i := strings.Index(f.Name, "/"); i > 0 {
				dir := f.Name[:i+1]
				if dirEntries[f.Name] || strings.Contains(f.Name[i+1:], "/") {
					topLevelDirs[dir] = true
				}
			}
		}
		if len(topLevelDirs) == 1 {
			for k := range topLevelDirs {
				rootDirPrefix = k
				foundPrefix = true
				log.Printf("Warning: Standard root directory pattern not found. Found single top-level directory '%s', assuming it's the root.", rootDirPrefix)
				break
			}
		}
	}
	if !foundPrefix {
		log.Println("Warning: Could not detect a single root package directory in the zip. Assuming files are relative to zip root.")
		rootDirPrefix = ""
	}

	relevantFiles := make(map[string]*zip.File)
	filePaths := []string{}
	rootInitPath := ""
	rootInitZipFile := (*zip.File)(nil)
	wallyTomlFile := (*zip.File)(nil)
	dependencies := make(map[string]PackageIdentifier)

	for _, f := range zipReader.File {
		if rootDirPrefix != "" && !strings.HasPrefix(f.Name, rootDirPrefix) {
			continue
		}
		relativePath := strings.TrimPrefix(f.Name, rootDirPrefix)
		if relativePath == "" || f.FileInfo().IsDir() {
			continue
		}

		baseName := filepath.Base(relativePath)
		dirName := filepath.Dir(relativePath)

		if baseName == "wally.toml" && dirName == "." {
			wallyTomlFile = f
			log.Printf("Found wally.toml file: %s", relativePath)
			continue
		}

		ext := filepath.Ext(relativePath)
		if !strings.HasPrefix(baseName, ".") && (ext == ".lua" || ext == ".luau") {
			if (baseName == "init.lua" || baseName == "init.luau") && dirName == "." {
				if rootInitPath != "" {
					log.Printf("Warning: Multiple root init files ('%s', '%s') found. Using '%s'.", rootInitPath, relativePath, rootInitPath)
				} else {
					rootInitPath = relativePath
					rootInitZipFile = f
					log.Printf("Found root init file: %s", rootInitPath)
				}
			} else {
				relevantFiles[relativePath] = f
				filePaths = append(filePaths, relativePath)
			}
		}
	}
	sort.Strings(filePaths)

	if wallyTomlFile != nil {
		rc, err := wallyTomlFile.Open()
		if err != nil {
			log.Printf("Warning: Could not open wally.toml: %v", err)
		} else {
			tomlData, err := io.ReadAll(rc)
			closeErr := rc.Close()
			if closeErr != nil {
				log.Printf("Warning: Failed to close wally.toml reader: %v", closeErr)
			}

			if err != nil {
				log.Printf("Warning: Could not read wally.toml: %v", err)
			} else {
				var wallyData WallyToml
				err = toml.Unmarshal(tomlData, &wallyData)
				if err != nil {
					log.Printf("Warning: Failed to parse wally.toml: %v", err)
				} else {
					log.Printf("Parsing %d dependencies from wally.toml", len(wallyData.Dependencies))
					for alias, depString := range wallyData.Dependencies {
						depScope, depName, depVersion, err := parseDependencyString(depString)
						if err != nil {
							log.Printf("Warning: Skipping invalid dependency '%s': %v", depString, err)
						} else {
							dependencies[alias] = PackageIdentifier{Scope: depScope, Name: depName, Version: depVersion}
							log.Printf("  - Parsed dependency: %s -> %s/%s@%s", alias, depScope, depName, depVersion)
						}
					}
				}
			}
		}
	}

	itemMap := make(map[string]*Item)
	rootItems := []*Item{}
	var singleRootModule *Item

	if rootInitPath != "" && rootInitZipFile != nil {
		rc, err := rootInitZipFile.Open()
		if err != nil {
			log.Printf("Warning: Could not open root init file %s: %v", rootInitZipFile.Name, err)
		} else {
			contentBytes, err := io.ReadAll(rc)
			closeErr := rc.Close()
			if closeErr != nil {
				log.Printf("Warning: Failed to close root init file reader: %v", closeErr)
			}

			if err != nil {
				log.Printf("Warning: Could not read root init file %s: %v", rootInitZipFile.Name, err)
			} else {
				content := string(contentBytes)
				log.Printf("Creating single root ModuleScript: %s", rootModuleName)
				singleRootModule = createModuleScriptItem(rootModuleName, content)
				rootItems = append(rootItems, singleRootModule)
			}
		}
	}

	for _, relPath := range filePaths {
		zipFile := relevantFiles[relPath]
		dirPath := filepath.Dir(relPath)
		if dirPath == "." {
			dirPath = ""
		}
		baseName := filepath.Base(relPath)
		itemName := strings.TrimSuffix(baseName, filepath.Ext(baseName))

		rc, err := zipFile.Open()
		if err != nil {
			log.Printf("Warning: Could not open file %s: %v. Skipping.", zipFile.Name, err)
			continue
		}
		contentBytes, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if closeErr != nil {
			log.Printf("Warning: Failed to close reader for %s: %v", zipFile.Name, closeErr)
		}

		if err != nil {
			log.Printf("Warning: Could not read file %s: %v. Skipping.", zipFile.Name, err)
			continue
		}
		content := string(contentBytes)

		parentContainer := findOrCreateParentItem(dirPath, itemMap, &rootItems, singleRootModule)
		isInitFile := baseName == "init.lua" || baseName == "init.luau"

		if isInitFile {
			if parentContainer == nil {
				log.Printf("Error: Found init file '%s' but could not determine parent container. Skipping.", relPath)
				continue
			}
			folderName := getName(parentContainer)
			log.Printf("Processing init file for item: %s (path: %s)", folderName, dirPath)
			if parentContainer.Class == "Folder" {
				log.Printf("Converting Folder '%s' path '%s' to ModuleScript", folderName, dirPath)
				parentContainer.Class = "ModuleScript"
				parentContainer.Properties = createModuleScriptProperties(folderName, content)
			} else if parentContainer.Class == "ModuleScript" {
				log.Printf("Warning: Parent '%s' path '%s' is already ModuleScript. Overwriting source from init file '%s'.", folderName, dirPath, relPath)
				parentContainer.Properties = createModuleScriptProperties(folderName, content)
			} else {
				log.Printf("Warning: Expected parent for init file '%s' to be Folder/ModuleScript, found '%s'. Skipping conversion.", relPath, parentContainer.Class)
			}
		} else {
			log.Printf("Processing regular script file: %s as ModuleScript '%s'", relPath, itemName)
			newItem := createModuleScriptItem(itemName, content)
			if parentContainer == nil {
				targetParent := singleRootModule
				parentName := "root level"
				if targetParent != nil {
					parentName = fmt.Sprintf("root module '%s'", getName(targetParent))
				}
				collision := false
				itemsToCheck := rootItems
				if targetParent != nil {
					itemsToCheck = targetParent.Children
				}

				for _, existingItem := range itemsToCheck {
					if getName(existingItem) == itemName {
						log.Printf("Warning: Item named '%s' already exists at %s. Skipping file '%s'.", itemName, parentName, relPath)
						collision = true
						break
					}
				}
				if !collision {
					if targetParent != nil {
						log.Printf("Adding '%s' as child to %s", itemName, parentName)
						targetParent.Children = append(targetParent.Children, newItem)
					} else {
						log.Printf("Adding '%s' as a top-level item", itemName)
						rootItems = append(rootItems, newItem)
					}
				}
			} else {
				log.Printf("Adding '%s' as child to container '%s' (path %s)", itemName, getName(parentContainer), dirPath)
				collision := false
				for _, existingChild := range parentContainer.Children {
					if getName(existingChild) == itemName {
						log.Printf("Warning: Child item named '%s' already exists under '%s' (path %s). Skipping file '%s'.", itemName, getName(parentContainer), dirPath, relPath)
						collision = true
						break
					}
				}
				if !collision {
					parentContainer.Children = append(parentContainer.Children, newItem)
				}
			}
		}
	}

	return rootItems, dependencies, nil
}

func parsePackageIdentifier(identifier string) (scope, name, version string, err error) {
	matches := packageIdentifierRegex.FindStringSubmatch(identifier)
	if len(matches) != 4 {
		return "", "", "", fmt.Errorf("Invalid format. Expected 'scope/name@version', got '%s'", identifier)
	}
	return matches[1], matches[2], matches[3], nil
}

func parseDependencyString(depStr string) (scope, name, version string, err error) {
	matches := dependencyRegex.FindStringSubmatch(depStr)
	if len(matches) < 3 {
		return "", "", "", fmt.Errorf("Invalid dependency format '%s'", depStr)
	}
	scope = matches[1]
	name = matches[2]

	if len(matches) == 4 && matches[3] != "" {
		versionPart := matches[3]
		originalVersionSpec := versionPart

		versionPart = strings.TrimPrefix(versionPart, "^")
		versionPart = strings.TrimPrefix(versionPart, "~")
		versionPart = strings.TrimPrefix(versionPart, ">=")
		versionPart = strings.TrimPrefix(versionPart, "<=")
		versionPart = strings.TrimPrefix(versionPart, ">")
		versionPart = strings.TrimPrefix(versionPart, "<")
		versionPart = strings.TrimSpace(versionPart)

		baseVersion := strings.Split(versionPart, "-")[0]
		baseVersion = strings.Split(baseVersion, "+")[0]
		versionParts := strings.Split(baseVersion, ".")
		numParts := len(versionParts)
		isNumeric := true
		for _, part := range versionParts {
			if part == "" || !regexp.MustCompile(`^\d+$`).MatchString(part) {
				isNumeric = false
				break
			}
		}

		if isNumeric {
			if numParts == 1 {
				version = fmt.Sprintf("%s.0.0", versionParts[0])
				log.Printf("Info: Expanded major version '%s' to '%s' for dependency '%s'", versionPart, version, depStr)
			} else if numParts == 2 {
				version = fmt.Sprintf("%s.%s.0", versionParts[0], versionParts[1])
				log.Printf("Info: Expanded major.minor version '%s' to '%s' for dependency '%s'", versionPart, version, depStr)
			} else {
				version = versionPart
				if version != originalVersionSpec && numParts >= 3 {
					log.Printf("Info: Using stripped version '%s' (from spec '%s') for dependency '%s'", version, originalVersionSpec, depStr)
				}
			}
		} else {
			version = versionPart
			if version != originalVersionSpec {
				log.Printf("Info: Using potentially complex/pre-release version '%s' (from spec '%s') for dependency '%s'", version, originalVersionSpec, depStr)
			}
		}

		if version == "" {
			return "", "", "", fmt.Errorf("Empty version found after parsing '%s'", depStr)
		}

	} else {
		return "", "", "", fmt.Errorf("Missing version specifier in dependency '%s'", depStr)
	}

	if scope == "" || name == "" || version == "" {
		return "", "", "", fmt.Errorf("Failed to fully parse dependency '%s' (scope=%s, name=%s, version=%s)", depStr, scope, name, version)
	}

	return scope, name, version, nil
}

func downloadZip(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("Failed to create download request: %w", err)
	}
	req.Header.Set("wally-version", SIMULATED_WALLY_VERSION) // Required to get the zip.
	log.Printf("Making GET request to %s with header 'wally-version: %s' (downloading)", url, SIMULATED_WALLY_VERSION)

	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Failed to execute download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("Package not found on Wally registry (404). Check scope, name, and version. Server response: %s", string(errorBody))
		}
		return nil, fmt.Errorf("Bad status code %d (%s). Server response: %s", resp.StatusCode, resp.Status, string(errorBody))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Failed to read download response: %w", err)
	}
	return body, nil
}

func findOrCreateParentItem(fullPath string, itemMap map[string]*Item, rootItems *[]*Item, rootModule *Item) *Item {
	if fullPath == "" {
		return nil
	}

	if item, exists := itemMap[fullPath]; exists {
		if item.Class == "Folder" || item.Class == "ModuleScript" {
			return item
		} else {
			log.Printf("Warning: Cached item for path '%s' exists but is a '%s', not a container.", fullPath, item.Class)
			return nil
		}
	}

	parentDirPath := filepath.Dir(fullPath)
	if parentDirPath == "." {
		parentDirPath = ""
	}
	folderName := filepath.Base(fullPath)
	var parentContainer *Item

	if parentDirPath == "" {
		if rootModule != nil {
			parentContainer = rootModule
		} else {
			var existingFolder *Item
			for _, rootItem := range *rootItems {
				if (rootItem.Class == "Folder" || rootItem.Class == "ModuleScript") && getName(rootItem) == folderName {
					existingFolder = rootItem
					break
				}
			}
			if existingFolder != nil {
				itemMap[fullPath] = existingFolder
				return existingFolder
			} else {
				log.Printf("Creating top-level Folder: %s", folderName)
				newFolder := createFolderItem(folderName)
				*rootItems = append(*rootItems, newFolder)
				itemMap[fullPath] = newFolder
				return newFolder
			}
		}
	} else {
		parentContainer = findOrCreateParentItem(parentDirPath, itemMap, rootItems, rootModule)
		if parentContainer == nil {
			log.Printf("Error: Could not find or create parent container for path '%s'. Cannot create child folder '%s'.", parentDirPath, folderName)
			return nil
		}
	}

	if parentContainer == nil {
		log.Printf("Error: Parent container is unexpectedly nil when trying to place folder '%s'.", folderName)
		return nil
	}

	var currentFolder *Item
	for _, child := range parentContainer.Children {
		if (child.Class == "Folder" || child.Class == "ModuleScript") && getName(child) == folderName {
			currentFolder = child
			break
		}
	}

	if currentFolder != nil {
		itemMap[fullPath] = currentFolder
		return currentFolder
	} else {
		parentContextName := getName(parentContainer)
		if parentDirPath == "" && rootModule != nil {
			parentContextName = fmt.Sprintf("root module (%s)", getName(rootModule))
		} else if parentDirPath != "" {
			parentContextName = fmt.Sprintf("'%s' (path %s)", getName(parentContainer), parentDirPath)
		}

		log.Printf("Creating nested Folder: %s (under %s)", folderName, parentContextName)
		currentFolder = createFolderItem(folderName)
		parentContainer.Children = append(parentContainer.Children, currentFolder)
		itemMap[fullPath] = currentFolder
		return currentFolder
	}
}

func getName(item *Item) string {
	if item == nil {
		return ""
	}
	for _, prop := range item.Properties {
		if p, ok := prop.(StringProperty); ok && p.Name == "Name" {
			return p.Value
		}
	}
	log.Printf("Warning: Could not find Name property for Item with Referent %s and Class %s", item.Referent, item.Class)
	return ""
}

func createFolderItem(name string) *Item {
	return &Item{
		Class:    "Folder",
		Referent: generateReferent(),
		Properties: []any{
			StringProperty{Name: "Name", Value: name},
			BinaryStringProperty{Name: "AttributesSerialize", Value: ""},
			BinaryStringProperty{Name: "Tags", Value: ""},
		},
		Children: []*Item{},
	}
}

func createModuleScriptItem(name, source string) *Item {
	return &Item{
		Class:      "ModuleScript",
		Referent:   generateReferent(),
		Properties: createModuleScriptProperties(name, source),
		Children:   []*Item{},
	}
}

func createModuleScriptProperties(name, source string) []any {
	return []any{
		StringProperty{Name: "Name", Value: name},
		ProtectedStringProperty{Name: "Source", Value: source},
		BinaryStringProperty{Name: "AttributesSerialize", Value: ""},
		SecurityCapabilitiesProperty{Name: "Capabilities", Value: 0},
		BoolProperty{Name: "DefinesCapabilities", Value: "false"},
		ContentProperty{Name: "LinkedSource", Null: &struct{}{}},
		StringProperty{Name: "ScriptGuid", Value: generateGuid()},
		Int64Property{Name: "SourceAssetId", Value: -1},
		BinaryStringProperty{Name: "Tags", Value: ""},
	}
}

func generateRBXMX(rootItems []*Item) ([]byte, error) {
	rbxmx := Roblox{
		XmlnsXsi:                     "http://www.w3.org/2001/XMLSchema-instance",
		XsiNoNamespaceSchemaLocation: "http://www.roblox.com/roblox.xsd",
		Version:                      "4",
		Meta: &Meta{
			Name:  "ExplicitAutoJoints",
			Value: "true",
		},
		Externals: []string{"nil", "null"},
		Items:     rootItems,
	}
	xmlBytes, err := xml.MarshalIndent(rbxmx, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("Failed to marshal RBXMX structure: %w", err)
	}
	fullXML := append([]byte(xml.Header), xmlBytes...)
	fullXML = append(fullXML, '\n')
	return fullXML, nil
}

func generateReferent() string {
	return "RBX" + strings.ToUpper(strings.ReplaceAll(uuid.New().String(), "-", ""))
}

func generateGuid() string {
	return "{" + strings.ToUpper(uuid.New().String()) + "}"
}
