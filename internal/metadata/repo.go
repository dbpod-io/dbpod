package metadata

// OfficialDownloadsBase is the base for resolving the relative package
// URLs stored in generated metadata: official files live under
// cdn.mysql.com/Downloads (mirrors substitute the base at download time).
const OfficialDownloadsBase = "https://cdn.mysql.com/Downloads"

// CDNURL builds the absolute official download URL for a package file.
func CDNURL(series, filename string) string {
	return OfficialDownloadsBase + "/MySQL-" + series + "/" + filename
}
