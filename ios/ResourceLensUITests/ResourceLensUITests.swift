import XCTest

final class ResourceLensUITests: XCTestCase {
    func testIndependentDownloadAndErrorHandling() {
        let app = XCUIApplication()
        app.launchArguments = ["--uitesting"]
        app.launch()
        XCTAssertTrue(app.staticTexts["iPhone 独立下载，无需连接电脑"].waitForExistence(timeout:15))
        app.buttons["添加下载链接"].tap()
        let input = app.textViews["download-input"]
        XCTAssertTrue(input.waitForExistence(timeout:5)); input.tap()
        input.typeText("http://127.0.0.1:18771/sample.png")
        app.buttons["识别并下载"].tap()
        XCTAssertTrue(app.staticTexts["已保存到 iPhone"].waitForExistence(timeout:30))
        XCTAssertTrue(app.buttons["分享文件"].exists)
        let shot = XCTAttachment(screenshot:app.screenshot()); shot.name="iPhone independent download"; shot.lifetime = .keepAlways; add(shot)
        app.buttons["新建下载"].tap()
        XCTAssertTrue(input.waitForExistence(timeout:5)); input.tap()
        input.typeText("https://example.com/song.mp3")
        app.buttons["识别并下载"].tap()
        XCTAssertTrue(app.staticTexts["source-error"].waitForExistence(timeout:30))
        app.buttons["取消"].tap()
        app.buttons["使用说明"].tap()
        XCTAssertTrue(app.staticTexts["使用时不需要电脑或配对码"].waitForExistence(timeout:5))
    }
}
