# AppBundleID Format Specification

An `AppBundleID` is required for every AppBundle to ensure proper functionality, such as generating the `mountDir` and enabling tools like `appstream-helper` to figure out info about the AppBundle. It can be non-compliant (i.e., not follow Type I, II, or III) if distribution via AppBundleHUB or `dbin` is not intended.

## Supported Formats

### Type I: Legacy Format
- **Structure**: `name-versionString-maintainer` or `name-date-maintainer`
- **Examples**: `steam-v128.0.3-xplshn`, `steam-20250101-xplshn`
- **Description**: Consists of the application name, either a version or date, and the maintainer/repository identifier. Suitable for filenames on systems with restrictive character support (e.g., no `#`, `:`).
- **Use Case**: Legacy distribution; preferred only for filenames

### Type II: Modern Format without Date
- **Structure**: `name#repo[:version]`
- **Examples**: `steam#xplshn:v128.0.3`, `steam#xplshn`
- **Description**: Includes the application name, repository/maintainer, and an optional version. Uses `#` to separate name and repository, with `:` for version.
- **Use Case**: Most preferred format, in its short-form version

### Type III: Modern Format with Optional Date
- **Structure**: `name#repo[:version][@date]`
- **Examples**: `steam#xplshn:v128.0.3@20250101`, `steam#xplshn@20250101`
- **Description**: The most flexible format, including application name, repository/maintainer, optional version, and optional date. Uses `#`, `:`, and `@` as separators.
- **Use Case**: Most preferred format, as it contains the most complete data for `appstream-helper` to parse

## Requirements and Usage

- **AppBundleHUB Distribution**: For inclusion in AppBundleHUB or `dbin` repositories, the `AppBundleID` must adhere to Type I, II, or III, as these formats allow `appstream-helper` to parse metadata (name/appstreamID, version, maintainer, date) for automated repository indexing.
- **Recommendation**: Type III is encouraged for the AppBundleID, as it is the most complete while Type I is recommended for AppBundle filenames on systems that may not support special characters like `#`, `:`, or `@` used in Type II and Type III.

# NOTEs

1. The program's AppStreamID should be used as the Name in the AppBundleID if the AppBundle does not ship with an .xml appstream metainfo file in the top-level of its AppDir. This way `appstream-helper` can use scrapped Flathub data to automatically populate a .description, .icon, .screenshots, .rank & .version entry for the `dbin` repository index file
2. Type I, II & III are all parsable by `appstream-helper`

