// iOS / macOS HMAC Implementation for Stream Authentication
// Add this to your Swift project

import Foundation
import CryptoKit

// MARK: - Stream Authentication

/// Configuration for stream authentication
struct StreamConfig {
    static let secret = "YOUR_STREAM_SECRET_HERE" // Same as backend STREAM_SECRET
    static let baseURL = "https://your-api.com" // Your backend URL
}

/// Generates signed URLs for streaming Telegram files
class StreamAuthenticator {
    
    /// Signs a stream URL with HMAC-SHA256
    /// - Parameters:
    ///   - messageId: The Telegram message ID
    ///   - expiresIn: Expiration time in seconds (default: 3600 = 1 hour)
    /// - Returns: Fully signed URL ready to use
    static func signStreamURL(messageId: Int, expiresIn: Int = 3600) -> String {
        let exp = Int(Date().timeIntervalSince1970) + expiresIn
        let data = "\(messageId):\(exp)"
        
        let key = SymmetricKey(data: Data(StreamConfig.secret.utf8))
        let signature = HMAC<SHA256>.authenticationCode(
            for: Data(data.utf8),
            using: key
        )
        let sig = Data(signature).map { String(format: "%02x", $0) }.joined()
        
        return "\(StreamConfig.baseURL)/direct/\(messageId)?sig=\(sig)&exp=\(exp)"
    }
    
    /// Generates only the signature components (useful if you need to build URL differently)
    /// - Parameters:
    ///   - messageId: The Telegram message ID
    ///   - expiresIn: Expiration time in seconds
    /// - Returns: Tuple with signature and expiration timestamp
    static func generateSignature(messageId: Int, expiresIn: Int = 3600) -> (signature: String, expiration: Int) {
        let exp = Int(Date().timeIntervalSince1970) + expiresIn
        let data = "\(messageId):\(exp)"
        
        let key = SymmetricKey(data: Data(StreamConfig.secret.utf8))
        let signature = HMAC<SHA256>.authenticationCode(
            for: Data(data.utf8),
            using: key
        )
        let sig = Data(signature).map { String(format: "%02x", $0) }.joined()
        
        return (sig, exp)
    }
}

// MARK: - Usage Examples

// Example 1: Simple usage with AVPlayer
/*
import AVKit

class VideoPlayerViewController: UIViewController {
    let messageId = 123
    
    override func viewDidLoad() {
        super.viewDidLoad()
        
        // Generate signed URL
        let urlString = StreamAuthenticator.signStreamURL(messageId: messageId)
        guard let url = URL(string: urlString) else { return }
        
        // Use with AVPlayer
        let player = AVPlayer(url: url)
        let playerViewController = AVPlayerViewController()
        playerViewController.player = player
        
        present(playerViewController, animated: true) {
            player.play()
        }
    }
}
*/

// Example 2: SwiftUI with VideoPlayer
/*
import SwiftUI
import AVKit

struct MediaPlayerView: View {
    let messageId: Int
    
    var streamURL: URL? {
        let urlString = StreamAuthenticator.signStreamURL(messageId: messageId)
        return URL(string: urlString)
    }
    
    var body: some View {
        if let url = streamURL {
            VideoPlayer(player: AVPlayer(url: url))
                .onAppear {
                    // Optional: preload or analytics
                }
        } else {
            Text("Invalid URL")
        }
    }
}
*/

// Example 3: Downloading with URLSession
/*
func downloadFile(messageId: Int, completion: @escaping (Result<URL, Error>) -> Void) {
    let urlString = StreamAuthenticator.signStreamURL(
        messageId: messageId,
        expiresIn: 7200 // 2 hours for download
    )
    
    guard let url = URL(string: urlString) else {
        completion(.failure(NSError(domain: "Invalid URL", code: -1)))
        return
    }
    
    let task = URLSession.shared.downloadTask(with: url) { localURL, response, error in
        if let error = error {
            completion(.failure(error))
            return
        }
        
        guard let localURL = localURL else {
            completion(.failure(NSError(domain: "No file downloaded", code: -1)))
            return
        }
        
        // Move to permanent location
        let documentsPath = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let destinationURL = documentsPath.appendingPathComponent("file_\(messageId).mp4")
        
        try? FileManager.default.removeItem(at: destinationURL)
        try? FileManager.default.moveItem(at: localURL, to: destinationURL)
        
        completion(.success(destinationURL))
    }
    task.resume()
}
*/

// Example 4: Custom expiration times
/*
// Short-lived URL (15 minutes) for preview
let previewURL = StreamAuthenticator.signStreamURL(messageId: 123, expiresIn: 900)

// Long-lived URL (24 hours) for offline viewing
let offlineURL = StreamAuthenticator.signStreamURL(messageId: 123, expiresIn: 86400)
*/

// MARK: - Security Best Practices

/*
1. Store the secret securely:
   - Use Keychain for storing STREAM_SECRET
   - Never hardcode in production builds
   - Consider using Info.plist with .gitignore

2. Handle expiration gracefully:
   - Regenerate URLs if they expire during playback
   - Show appropriate error messages to users
   
3. Network considerations:
   - URLs are one-time use but can be reused before expiration
   - Each new URL generation creates a new expiration window
   
4. Testing:
   - Test with short expiration (60s) during development
   - Verify signature generation matches backend
*/

// MARK: - Keychain Storage (Optional but Recommended)

/*
import Security

class SecureStorage {
    static func saveSecret(_ secret: String) {
        let data = secret.data(using: .utf8)!
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: "stream_secret",
            kSecValueData as String: data
        ]
        
        SecItemDelete(query as CFDictionary)
        SecItemAdd(query as CFDictionary, nil)
    }
    
    static func loadSecret() -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: "stream_secret",
            kSecReturnData as String: true
        ]
        
        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        
        guard status == errSecSuccess,
              let data = result as? Data,
              let secret = String(data: data, encoding: .utf8) else {
            return nil
        }
        
        return secret
    }
}

// Usage:
// SecureStorage.saveSecret("your-secret-here")
// StreamConfig.secret = SecureStorage.loadSecret() ?? ""
*/
