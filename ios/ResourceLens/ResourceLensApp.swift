import SwiftUI
import UIKit

final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String, completionHandler: @escaping () -> Void) {
        DownloadStore.shared.handleBackground(identifier, completion: completionHandler)
    }
}

@main struct ResourceLensApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) var delegate
    @StateObject private var store = DownloadStore.shared
    var body: some Scene { WindowGroup { HomeView().environmentObject(store).tint(Color(red:0.02, green:0.52, blue:0.58)) } }
}
