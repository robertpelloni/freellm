' FreeLLM Watchdog - Silent background process monitor
' Runs freellm.exe, restarts if crashed or duplicate instances found
' No console window (runs via WScript, not CScript)

Set WshShell = CreateObject("WScript.Shell")
Dim strPath
strPath = "C:\Users\hyper\workspace\freellm"

WScript.Sleep 5000 ' Give freellm time to start on boot

Do While True
    ' Check how many freellm.exe instances are running
    Dim strCmd, strResult, intCount
    
    ' Use WMIC to count processes (hidden, no window flash)
    strCmd = "cmd /c """ & strPath & "\watchdog.bat""" ' Fallback: we'll use a simpler wmic approach
    
    ' Count processes via WMIC directly (no cmd.exe flash)
    strCmd = "%SYSTEMROOT%\System32\wbem\WMIC.exe process where ""name='freellm.exe'"" get ProcessId"
    strResult = WshShell.Exec(strCmd).StdOut.ReadAll()
    
    ' Count lines (each PID = one instance, minus header rows)
    intCount = 0
    Dim lines, line
    lines = Split(strResult, vbCrLf)
    For Each line In lines
        If Trim(line) <> "" And IsNumeric(Trim(line)) Then
            intCount = intCount + 1
        End If
    Next
    
    ' Strip header lines
    intCount = intCount - 1 ' Remove the first numeric match if it's a header
    
    ' Actually count properly: count PIDs
    If InStr(strResult, "ProcessId") Then
        strResult = Mid(strResult, InStr(strResult, vbCrLf) + 2)
    End If
    
    intCount = 0
    Dim pidLines
    pidLines = Split(strResult, vbCrLf)
    For Each line In pidLines
        If Trim(line) <> "" Then
            intCount = intCount + 1
        End If
    Next
    
    ' If duplicate PIDs from wmic header
    If intCount > 0 Then
        ' Check if any non-numeric lines (header)
        Dim checkCount
        checkCount = 0
        For Each line In pidLines
            If IsNumeric(Trim(line)) Then
                checkCount = checkCount + 1
            End If
        Next
        intCount = checkCount
    End If
    
    If intCount > 1 Then
        ' Too many instances - kill all and restart
        WshShell.Run "%SYSTEMROOT%\System32\taskkill.exe /F /IM freellm.exe", 0, True
        WScript.Sleep 5000
        WshShell.Run Chr(34) & strPath & "\freellm.exe" & Chr(34), 0, False
        WScript.Sleep 8000
    ElseIf intCount = 0 Then
        ' Not running - start it
        WshShell.Run Chr(34) & strPath & "\freellm.exe" & Chr(34), 0, False
        WScript.Sleep 10000
    End If
    
    ' Wait 30 seconds before next check
    WScript.Sleep 30000
Loop
