import Foundation
import IOKit.pwr_mgt

/// Prevents the system from idle-sleeping using an IOKit power-management
/// assertion — the same native mechanism macOS itself uses (no caffeinate
/// subprocess, no helper, no extra permissions required).
final class SleepPreventer {
    private var assertionID: IOPMAssertionID = 0
    private var isHeld = false

    func start() {
        guard !isHeld else { return }
        let result = IOPMAssertionCreateWithName(
            kIOPMAssertionTypeNoIdleSleep as CFString,
            IOPMAssertionLevel(kIOPMAssertionLevelOn),
            "StreamDVR is recording live streams" as CFString,
            &assertionID)
        if result == kIOReturnSuccess {
            isHeld = true
        }
    }

    func stop() {
        guard isHeld else { return }
        IOPMAssertionRelease(assertionID)
        isHeld = false
    }
}