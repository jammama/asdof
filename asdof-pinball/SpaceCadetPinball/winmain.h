#pragma once
#include "gdrv.h"

enum class GameBindings;

struct SdlTickClock
{
	using duration = std::chrono::milliseconds;
	using rep = duration::rep;
	using period = duration::period;
	using time_point = std::chrono::time_point<SdlTickClock>;
	static constexpr bool is_steady = true;

	static time_point now() noexcept
	{
		return time_point{duration{SDL_GetTicks()}};
	}
};

struct SdlPerformanceClock
{
	using duration = std::chrono::duration<uint64_t, std::nano>;
	using rep = duration::rep;
	using period = duration::period;
	using time_point = std::chrono::time_point<SdlPerformanceClock>;
	static constexpr bool is_steady = true;

	static time_point now() noexcept
	{
		const auto freq = SDL_GetPerformanceFrequency();
		const auto ctr = SDL_GetPerformanceCounter();
		const auto whole = (ctr / freq) * period::den;
		const auto part = (ctr % freq) * period::den / freq;
		return time_point(duration(whole + part));
	}
};

struct WelfordState
{
	double mean;
	double M2;
	int64_t count;

	WelfordState() : mean(0.005), M2(0), count(1)
	{
	}

	void Advance(double newValue)
	{
		++count;
		auto delta = newValue - mean;
		mean += delta / count;
		M2 += delta * (newValue - mean); //M2n = M2n-1 + (Xn - AvgXn-1) * (Xn - AvgXn)
	}

	double GetStdDev() const
	{
		return std::sqrt(M2 / (count - 1)); // Sn^2 = M2n / (n - 1)
	}
};

class winmain
{
	using Clock = SdlPerformanceClock; // Or std::chrono::steady_clock.
	using DurationMs = std::chrono::duration<double, std::milli>;
	using TimePoint = std::chrono::time_point<Clock>;

public:
	static constexpr const char* Version = "2.1.1 DEV";
	static bool single_step;
	static SDL_Window* MainWindow;
	static SDL_Renderer* Renderer;
	static ImGuiIO* ImIO;
	static ImFont* FontBig; // 인게임 텍스트박스용 큰 폰트(웹). null 이면 기본 폰트 사용.
	static bool LaunchBallEnabled;
	static bool HighScoresEnabled;
	static bool DemoActive;
	static int MainMenuHeight;

	static int WinMain(LPCSTR lpCmdLine);
	static int event_handler(const SDL_Event* event);
	[[ noreturn ]] static void memalloc_failure();
	static int ProcessWindowMessages();
	static void a_dialog();
	static void end_pause();
	static void new_game();
	static void pause(bool toggle = true);
	static void Restart();
	static void UpdateFrameRate();
	static void HandleGameBinding(GameBindings binding, bool shortcut);
private:
	static int return_value;
	static int mouse_down, last_mouse_x, last_mouse_y;
	static bool no_time_loss, activated, bQuit, has_focus, DispGRhistory, DispFrameRate;
	static std::vector<float> gfrDisplay;
	static std::string FpsDetails, PrevSdlError;
	static bool restart;
	static bool ShowAboutDialog;
	static bool ShowImGuiDemo;
	static bool ShowSpriteViewer;
	static bool ShowExitPopup;
	static double UpdateToFrameRatio;
	static DurationMs TargetFrameTime;
	static struct optionsStruct& Options;
	static DurationMs SpinThreshold;
	static WelfordState SleepState;
	static unsigned PrevSdlErrorCount;
	static unsigned gfrOffset;
	static float gfrWindow;
	static int CursorIdleCounter;

	// MainLoop timing state. Formerly locals of MainLoop(); promoted to statics so that
	// MainLoopTick() can keep them across calls when the browser drives one tick per frame.
	static unsigned updateCounter, frameCounter;
	static double UpdateToFrameCounter;
	static DurationMs sleepRemainder, frameDuration;
	static TimePoint frameStart, prevTime;

	static void RenderUi();
	static void RenderFrameTimeDialog();
	static void HybridSleep(DurationMs seconds);
	static void MainLoop();
	// One iteration of the game loop. On desktop it runs in a while loop; in the browser
	// it is registered with emscripten_set_main_loop and called once per animation frame.
	static void MainLoopTick();
	static void ImGuiMenuItemWShortcut(GameBindings binding, bool selected = false);
};
