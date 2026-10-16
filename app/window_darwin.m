#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

void ResourceLensInsetWebView(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        NSWindow *window = NSApp.mainWindow ?: NSApp.keyWindow;
        if (!window) {
            for (NSWindow *candidate in NSApp.windows) {
                if (candidate.canBecomeMainWindow && candidate.contentView) {
                    window = candidate;
                    break;
                }
            }
        }
        // Wails fills the full-size content view, including the title bar.
        // Leave that strip to AppKit. The native visual-effect view still fills
        // the whole window; only the webview follows the content layout guide.
        NSLayoutGuide *layout = window.contentLayoutGuide;
        for (NSView *view in window.contentView.subviews) {
            if (![view isKindOfClass:[WKWebView class]] || !view.translatesAutoresizingMaskIntoConstraints) continue;
            view.translatesAutoresizingMaskIntoConstraints = NO;
            [NSLayoutConstraint activateConstraints:@[
                [view.leadingAnchor constraintEqualToAnchor:window.contentView.leadingAnchor],
                [view.trailingAnchor constraintEqualToAnchor:window.contentView.trailingAnchor],
                [view.topAnchor constraintEqualToAnchor:layout.topAnchor],
                [view.bottomAnchor constraintEqualToAnchor:window.contentView.bottomAnchor]
            ]];
        }
    });
}
